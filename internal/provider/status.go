// Copyright (C) 2026 The OpenEverest Contributors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package provider

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	valkeyv1alpha1 "github.com/valkey-io/valkey-operator/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openeverest/provider-valkey/internal/common"
)

// Reasons explaining why clients cannot connect.
const (
	// reasonNoPrimary: a shard has no node that reports the primary role, so
	// the hash slots it serves cannot be read or written.
	reasonNoPrimary = "NoPrimary"
	// reasonSlotsUnassigned: the operator found hash slots that no primary
	// serves.
	reasonSlotsUnassigned = valkeyv1alpha1.ReasonSlotsUnassigned
)

// reasonRollingOutImage is the reason of the Upgrading operation.
const reasonRollingOutImage = "RollingOutImage"

// operatorGenericWait is the message the operator puts on its Ready condition
// for several steps of forming the cluster. Its Progressing condition then
// names the step.
const operatorGenericWait = "Cluster is Reconciling"

// errFragmentConflict is how the API server refuses a write made from a stale
// read. The operator fails its pass on one and retries on the next.
const errFragmentConflict = "the object has been modified"

// readyErrorReasons are the reasons with which the operator turns its Ready
// condition False because a step of its pass failed, as opposed to while it
// waits for the cluster to form or roll.
var readyErrorReasons = []string{
	valkeyv1alpha1.ReasonServiceError,
	valkeyv1alpha1.ReasonPodDisruptionBudgetError,
	valkeyv1alpha1.ReasonUsersAclError,
	valkeyv1alpha1.ReasonConfigMapError,
	valkeyv1alpha1.ReasonTrustBundleError,
	valkeyv1alpha1.ReasonValkeyNodeListError,
	valkeyv1alpha1.ReasonSystemUsersAclError,
	valkeyv1alpha1.ReasonValkeyNodeError,
	valkeyv1alpha1.ReasonOperatorAuthenticationFailed,
	valkeyv1alpha1.ReasonScaleInBlocked,
}

// Status reports what the provider observes about the cluster: whether it
// runs what the Instance asks for, whether clients can connect, what is wrong
// and which long operation is running. The runtime derives the Instance's
// phase and conditions from these facts.
func (p *Provider) Status(c *controller.Context) (controller.Status, error) {
	vc, err := uncachedCluster(c)
	if apierrors.IsNotFound(err) {
		return withDesiredReplicas(c, controller.Progressing("Waiting for the ValkeyCluster object to be created")), nil
	}
	if err != nil {
		return controller.Status{}, err
	}

	nodes := &valkeyv1alpha1.ValkeyNodeList{}
	if err := c.List(nodes, client.MatchingLabels{clusterLabel: c.Name()}); err != nil {
		return controller.Status{}, fmt.Errorf("list ValkeyNodes of cluster %q: %w", c.Name(), err)
	}

	status := withDesiredReplicas(c, observe(vc, liveNodes(nodes.Items)))

	// The connection details are reported as soon as clients can connect,
	// not only once the cluster has converged: whoever is told the database
	// is available must find where to connect, and the details must follow a
	// change that is still rolling out.
	if !status.Converged && status.Available != controller.Available {
		return status, nil
	}

	details, err := buildConnectionDetails(c)
	if err != nil {
		return controller.Status{}, fmt.Errorf("build connection details: %w", err)
	}
	status.ConnectionDetails = details
	return status, nil
}

// uncachedCluster reads the ValkeyCluster object from the API server instead
// of the cache. Sync has just applied it, and the cache may still hold the
// object from before that: its Ready condition would then look caught up with
// a generation that is no longer the latest, and the Instance would read as
// ready for a change the operator has not seen. The client reads unstructured
// objects without the cache.
func uncachedCluster(c *controller.Context) (*valkeyv1alpha1.ValkeyCluster, error) {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(valkeyv1alpha1.GroupVersion.WithKind("ValkeyCluster"))
	if err := c.Get(obj, c.Name()); err != nil {
		return nil, fmt.Errorf("get ValkeyCluster %q: %w", c.Name(), err)
	}

	vc := &valkeyv1alpha1.ValkeyCluster{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, vc); err != nil {
		return nil, fmt.Errorf("decode ValkeyCluster %q: %w", c.Name(), err)
	}
	return vc, nil
}

// withDesiredReplicas reports how many pods each component will have once the
// cluster runs what the Instance asks for. The count comes from the Instance,
// with the defaults Sync applies, not from the ValkeyCluster object, which may
// still describe an earlier spec. The monitoring component runs as a sidecar
// in the Valkey pods and has no pods of its own.
func withDesiredReplicas(c *controller.Context, status controller.Status) controller.Status {
	if _, ok := c.Instance().Spec.Components[common.ComponentEngine]; ok {
		status = status.WithDesiredReplicas(common.ComponentEngine, enginePods(c.Instance()))
	}
	return status
}

// liveNodes returns the ValkeyNodes that are not being deleted, sorted by
// name. A scale-in deletes the nodes of the shards it has drained.
func liveNodes(nodes []valkeyv1alpha1.ValkeyNode) []valkeyv1alpha1.ValkeyNode {
	live := slices.DeleteFunc(slices.Clone(nodes), func(n valkeyv1alpha1.ValkeyNode) bool {
		return !n.DeletionTimestamp.IsZero()
	})
	slices.SortFunc(live, func(a, b valkeyv1alpha1.ValkeyNode) int { return strings.Compare(a.Name, b.Name) })
	return live
}

// observe turns the ValkeyCluster object and its ValkeyNodes into the facts
// the provider reports.
func observe(vc *valkeyv1alpha1.ValkeyCluster, nodes []valkeyv1alpha1.ValkeyNode) controller.Status {
	var status controller.Status
	status.Available, status.AvailableReason, status.AvailableMessage = availability(vc, nodes)

	for _, problem := range problems(vc, nodes) {
		status = status.WithProblem(problem.Reason, problem.Message)
	}

	status.Message = waitingFor(vc, nodes)
	status.Converged = status.Message == ""
	if status.Converged {
		return status
	}

	// The operator moves the nodes to a new spec one at a time, so a node
	// that still has another image means an engine version change is rolling
	// out.
	if message, rolling := imageRollout(vc, nodes); rolling {
		status = status.WithOperation(corev1alpha1.ConditionUpgrading, reasonRollingOutImage, message)
	}
	if progress, resizing := volumeResize(vc, nodes); resizing {
		status = status.WithOperation(corev1alpha1.ConditionStorageResizing, corev1alpha1.ReasonStorageExpansionTriggered,
			"Resizing the volumes of the Valkey nodes "+progress)
	}
	return status
}

// waitingFor says what keeps the cluster from running what the Instance asks
// for, in the operator's own words where it has any. It returns "" when
// nothing does: the operator reports the cluster healthy for the latest
// generation, and every ValkeyNode runs the cluster's spec, is ready and has
// a volume of the requested size.
func waitingFor(vc *valkeyv1alpha1.ValkeyCluster, nodes []valkeyv1alpha1.ValkeyNode) string {
	ready := meta.FindStatusCondition(vc.Status.Conditions, valkeyv1alpha1.ConditionReady)
	degraded := meta.FindStatusCondition(vc.Status.Conditions, valkeyv1alpha1.ConditionDegraded)
	switch {
	case ready == nil:
		return "Waiting for the operator to start creating the cluster"
	case ready.ObservedGeneration < vc.Generation:
		return "Waiting for the operator to apply the latest configuration"
	case ready.Status != metav1.ConditionTrue:
		return operatorWait(vc, ready)
	case degraded != nil && degraded.Status == metav1.ConditionTrue:
		return "The operator reports the cluster degraded: " + cmp.Or(degraded.Message, degraded.Reason)
	}

	// The operator waits for each node it updates before it reports the
	// cluster ready, but its view of the nodes and the provider's cache are
	// read at different times.
	for i := range nodes {
		node := &nodes[i]
		switch {
		case !nodeSpecCurrent(vc, node) || node.Status.ObservedGeneration < node.Generation:
			return fmt.Sprintf("Waiting for the operator to update ValkeyNode %s", node.Name)
		case !node.Status.Ready:
			return fmt.Sprintf("Waiting for ValkeyNode %s to become ready", node.Name)
		}
	}

	// The operator grows the volumes without restarting the pods, so it
	// reports the cluster ready while they grow.
	if progress, resizing := volumeResize(vc, nodes); resizing {
		return "Waiting for the volumes of the Valkey nodes to grow " + progress
	}
	return ""
}

// operatorWait says what the operator waits for while it does not report the
// cluster ready, from its Ready condition, or from its Progressing condition
// when Ready only says that the cluster is reconciling.
func operatorWait(vc *valkeyv1alpha1.ValkeyCluster, ready *metav1.Condition) string {
	switch {
	case isConflict(ready.Message):
		return "Waiting for the operator to retry a conflicting write"
	case slices.Contains(readyErrorReasons, ready.Reason):
		return "The operator reports an error: " + cmp.Or(ready.Message, ready.Reason)
	}
	if ready.Message == operatorGenericWait {
		progressing := meta.FindStatusCondition(vc.Status.Conditions, valkeyv1alpha1.ConditionProgressing)
		if progressing != nil && progressing.Status == metav1.ConditionTrue && progressing.Message != "" {
			return progressing.Message
		}
	}
	return cmp.Or(ready.Message, "Waiting for the operator to report the cluster ready")
}

// problems returns what the operator reports wrong with the cluster for its
// latest generation: a step of its pass that failed, a Degraded condition,
// and a volume that cannot grow. A write conflict is retried on the
// operator's next pass, and the runtime reports unschedulable pods from the
// pods themselves, so neither is a problem here.
func problems(vc *valkeyv1alpha1.ValkeyCluster, nodes []valkeyv1alpha1.ValkeyNode) []controller.Problem {
	var found []controller.Problem
	add := func(reason, message string) {
		if !slices.ContainsFunc(found, func(p controller.Problem) bool { return p.Reason == reason }) {
			found = append(found, controller.Problem{Reason: reason, Message: message})
		}
	}
	current := func(cond *metav1.Condition) bool {
		return cond != nil && cond.ObservedGeneration == vc.Generation && !isConflict(cond.Message)
	}

	ready := meta.FindStatusCondition(vc.Status.Conditions, valkeyv1alpha1.ConditionReady)
	if current(ready) && ready.Status == metav1.ConditionFalse && slices.Contains(readyErrorReasons, ready.Reason) {
		add(ready.Reason, cmp.Or(ready.Message, "The operator reports "+ready.Reason))
	}
	degraded := meta.FindStatusCondition(vc.Status.Conditions, valkeyv1alpha1.ConditionDegraded)
	if current(degraded) && degraded.Status == metav1.ConditionTrue && degraded.Reason != valkeyv1alpha1.ReasonPodUnschedulable {
		add(degraded.Reason, cmp.Or(degraded.Message, "The operator reports the cluster degraded"))
	}

	for i := range nodes {
		size := meta.FindStatusCondition(nodes[i].Status.Conditions, valkeyv1alpha1.ValkeyNodeConditionPersistentVolumeClaimSizeReady)
		if size != nil && size.Status == metav1.ConditionFalse && size.Reason == valkeyv1alpha1.ValkeyNodeReasonPersistentVolumeClaimResizeInfeasible {
			add(corev1alpha1.ReasonStorageResizeFailed, size.Message)
		}
	}
	return found
}

// isConflict reports whether an operator message is a write conflict.
func isConflict(message string) bool {
	return strings.Contains(message, errFragmentConflict)
}

// nodeSpecCurrent reports whether a ValkeyNode runs the image and volume size
// of the cluster's spec.
func nodeSpecCurrent(vc *valkeyv1alpha1.ValkeyCluster, node *valkeyv1alpha1.ValkeyNode) bool {
	if node.Spec.Image != vc.Spec.Image {
		return false
	}
	want, have := vc.Spec.Persistence, node.Spec.Persistence
	if want == nil || have == nil {
		return want == nil && have == nil
	}
	return want.Size.Cmp(have.Size) == 0
}

// imageRollout says how far the operator is in moving the nodes to the
// cluster's image, and whether any node still has another one.
func imageRollout(vc *valkeyv1alpha1.ValkeyCluster, nodes []valkeyv1alpha1.ValkeyNode) (string, bool) {
	updated := 0
	for i := range nodes {
		if nodes[i].Spec.Image == vc.Spec.Image {
			updated++
		}
	}
	if updated == len(nodes) {
		return "", false
	}
	return fmt.Sprintf("Rolling out image %s (%d of %d nodes updated)", vc.Spec.Image, updated, len(nodes)), true
}

// volumeResize says how far the volumes of the nodes are in growing to the
// requested size, and whether any has not reached it. The operator reports it
// per node; a claim that is not bound yet is being created, not resized.
func volumeResize(vc *valkeyv1alpha1.ValkeyCluster, nodes []valkeyv1alpha1.ValkeyNode) (string, bool) {
	if vc.Spec.Persistence == nil {
		return "", false
	}
	pending := 0
	for i := range nodes {
		conds := nodes[i].Status.Conditions
		if meta.IsStatusConditionTrue(conds, valkeyv1alpha1.ValkeyNodeConditionPersistentVolumeClaimReady) &&
			meta.IsStatusConditionFalse(conds, valkeyv1alpha1.ValkeyNodeConditionPersistentVolumeClaimSizeReady) {
			pending++
		}
	}
	if pending == 0 {
		return "", false
	}
	return fmt.Sprintf("to %s (%d of %d not resized yet)", vc.Spec.Persistence.Size.String(), pending, len(nodes)), true
}

// availability says whether clients can connect: every shard that serves hash
// slots needs a node that reports the primary role. The operator records the
// role of each node on its ValkeyNode, asking the node itself, and clears it
// as soon as the pod is not ready.
//
// A shard serves slots once the operator has formed it, and the operator
// creates the ValkeyNodes of a shard before the shard can serve any, so the
// shards that count are those with ValkeyNodes, including the shards a
// scale-in still drains. The first shard always counts: every formed cluster
// has it.
func availability(vc *valkeyv1alpha1.ValkeyCluster, nodes []valkeyv1alpha1.ValkeyNode) (controller.Availability, string, string) {
	slots := meta.FindStatusCondition(vc.Status.Conditions, valkeyv1alpha1.ConditionSlotsAssigned)
	switch {
	case slots == nil || slots.Status == metav1.ConditionUnknown:
		// The operator has not formed the cluster yet. The runtime knows
		// whether it has ever been available.
		return controller.AvailabilityUnreported, "", ""
	case slots.Status == metav1.ConditionFalse:
		return controller.Unavailable, reasonSlotsUnassigned, "Some hash slots are not served by any primary"
	}

	if missing := shardsWithoutPrimary(nodes); len(missing) > 0 {
		return controller.Unavailable, reasonNoPrimary, noPrimaryMessage(vc, missing)
	}
	return controller.Available, "", ""
}

// shardsWithoutPrimary returns, sorted, the shards that count for
// availability and have no node that reports the primary role.
func shardsWithoutPrimary(nodes []valkeyv1alpha1.ValkeyNode) []int {
	counted := map[int]bool{0: false}
	for i := range nodes {
		shard, err := strconv.Atoi(nodes[i].Labels[shardLabel])
		if err != nil {
			continue
		}
		counted[shard] = counted[shard] || nodes[i].Status.Role == primaryRole
	}

	var missing []int
	for shard, hasPrimary := range counted {
		if !hasPrimary {
			missing = append(missing, shard)
		}
	}
	slices.Sort(missing)
	return missing
}

// noPrimaryMessage names the shards without a primary. A single-shard cluster,
// the replication topology, has no shards to name.
func noPrimaryMessage(vc *valkeyv1alpha1.ValkeyCluster, missing []int) string {
	if vc.Spec.Shards <= 1 && len(missing) == 1 && missing[0] == 0 {
		return "No node reports the primary role"
	}
	names := make([]string, len(missing))
	for i, shard := range missing {
		names[i] = strconv.Itoa(shard)
	}
	if len(names) == 1 {
		return fmt.Sprintf("No node of shard %s reports the primary role", names[0])
	}
	return fmt.Sprintf("No node of shards %s reports the primary role", strings.Join(names, ", "))
}
