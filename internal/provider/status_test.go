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
	"strconv"
	"testing"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	valkeyv1alpha1 "github.com/valkey-io/valkey-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/openeverest/provider-valkey/internal/common"
)

const (
	statusTestGeneration = 4
	statusTestImage      = "valkey/valkey:9.0.6"
)

// condition is a condition the operator set on its pass over the given
// generation.
func condition(condType string, status metav1.ConditionStatus, reason, message string, generation int64) metav1.Condition {
	return metav1.Condition{Type: condType, Status: status, Reason: reason, Message: message, ObservedGeneration: generation}
}

// healthyCluster returns a ValkeyCluster with the given number of shards that
// the operator reports healthy for its latest generation. Tests change what
// they are about.
func healthyCluster(shards int32, changes ...func(*valkeyv1alpha1.ValkeyCluster)) *valkeyv1alpha1.ValkeyCluster {
	vc := &valkeyv1alpha1.ValkeyCluster{
		ObjectMeta: metav1.ObjectMeta{Name: testInstance, Namespace: testNamespace, Generation: statusTestGeneration},
		Spec: valkeyv1alpha1.ValkeyClusterSpec{
			Shards:           shards,
			ReplicasPerShard: 1,
			Image:            statusTestImage,
			Persistence:      &valkeyv1alpha1.PersistenceSpec{Size: resource.MustParse("10Gi")},
		},
		Status: valkeyv1alpha1.ValkeyClusterStatus{
			State:       valkeyv1alpha1.ClusterStateReady,
			Shards:      shards,
			ReadyShards: shards,
			Conditions: []metav1.Condition{
				condition(valkeyv1alpha1.ConditionReady, metav1.ConditionTrue, valkeyv1alpha1.ReasonClusterHealthy, "Cluster is healthy", statusTestGeneration),
				condition(valkeyv1alpha1.ConditionProgressing, metav1.ConditionFalse, valkeyv1alpha1.ReasonReconcileComplete, "No changes needed", statusTestGeneration),
				condition(valkeyv1alpha1.ConditionClusterFormed, metav1.ConditionTrue, valkeyv1alpha1.ReasonTopologyComplete, "All nodes joined cluster", statusTestGeneration),
				condition(valkeyv1alpha1.ConditionSlotsAssigned, metav1.ConditionTrue, valkeyv1alpha1.ReasonAllSlotsAssigned, "All slots assigned", statusTestGeneration),
			},
		},
	}
	for _, change := range changes {
		change(vc)
	}
	return vc
}

// withCondition replaces or adds a condition of the cluster.
func withCondition(c metav1.Condition) func(*valkeyv1alpha1.ValkeyCluster) {
	return func(vc *valkeyv1alpha1.ValkeyCluster) {
		for i := range vc.Status.Conditions {
			if vc.Status.Conditions[i].Type == c.Type {
				vc.Status.Conditions[i] = c
				return
			}
		}
		vc.Status.Conditions = append(vc.Status.Conditions, c)
	}
}

// withoutCondition drops a condition of the cluster.
func withoutCondition(condType string) func(*valkeyv1alpha1.ValkeyCluster) {
	return func(vc *valkeyv1alpha1.ValkeyCluster) {
		var kept []metav1.Condition
		for _, c := range vc.Status.Conditions {
			if c.Type != condType {
				kept = append(kept, c)
			}
		}
		vc.Status.Conditions = kept
	}
}

// shardNode returns a ready, caught-up ValkeyNode of the given shard and node index
// with the given role, running the cluster's spec. Tests change what they are
// about.
func shardNode(shard, index int, role string, changes ...func(*valkeyv1alpha1.ValkeyNode)) valkeyv1alpha1.ValkeyNode {
	n := valkeyv1alpha1.ValkeyNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:       testInstance + "-" + strconv.Itoa(shard) + "-" + strconv.Itoa(index),
			Namespace:  testNamespace,
			Generation: 2,
			Labels: map[string]string{
				clusterLabel:   testInstance,
				shardLabel:     strconv.Itoa(shard),
				nodeIndexLabel: strconv.Itoa(index),
			},
		},
		Spec: valkeyv1alpha1.ValkeyNodeSpec{
			Image:       statusTestImage,
			Persistence: &valkeyv1alpha1.PersistenceSpec{Size: resource.MustParse("10Gi")},
		},
		Status: valkeyv1alpha1.ValkeyNodeStatus{ObservedGeneration: 2, Ready: true, Role: role},
	}
	for _, change := range changes {
		change(&n)
	}
	return n
}

// shardNodes returns a primary and a replica for each of the shards.
func shardNodes(shards int) []valkeyv1alpha1.ValkeyNode {
	var nodes []valkeyv1alpha1.ValkeyNode
	for shard := range shards {
		nodes = append(nodes, shardNode(shard, 0, primaryRole), shardNode(shard, 1, "replica"))
	}
	return nodes
}

// down marks a ValkeyNode whose pod is not ready: the operator clears its role.
func down(n *valkeyv1alpha1.ValkeyNode) {
	n.Status.Ready = false
	n.Status.Role = ""
}

// volume sets the conditions the operator reports about a node's volume.
func volume(sizeStatus metav1.ConditionStatus, sizeReason, message string) func(*valkeyv1alpha1.ValkeyNode) {
	return func(n *valkeyv1alpha1.ValkeyNode) {
		n.Status.Conditions = []metav1.Condition{
			condition(valkeyv1alpha1.ValkeyNodeConditionPersistentVolumeClaimReady, metav1.ConditionTrue, "PersistentVolumeClaimBound", "bound", n.Generation),
			condition(valkeyv1alpha1.ValkeyNodeConditionPersistentVolumeClaimSizeReady, sizeStatus, sizeReason, message, n.Generation),
		}
	}
}

func TestObserve(t *testing.T) {
	t.Parallel()

	conflict := `Operation cannot be fulfilled on valkeynodes.valkey.io "cache-0-1": the object has been modified; please apply your changes to the latest version and try again`

	tests := []struct {
		name  string
		vc    *valkeyv1alpha1.ValkeyCluster
		nodes []valkeyv1alpha1.ValkeyNode

		converged  bool
		message    string
		available  controller.Availability
		availWhy   string
		availMsg   string
		problems   []controller.Problem
		operations []controller.Operation
	}{
		{
			name:      "healthy and caught up",
			vc:        healthyCluster(3),
			nodes:     shardNodes(3),
			converged: true,
			available: controller.Available,
		},
		{
			name: "not picked up by the operator",
			vc: healthyCluster(1, func(vc *valkeyv1alpha1.ValkeyCluster) {
				vc.Status = valkeyv1alpha1.ValkeyClusterStatus{}
			}),
			message: "Waiting for the operator to start creating the cluster",
		},
		{
			name: "forming: shards missing",
			vc: healthyCluster(3,
				withoutCondition(valkeyv1alpha1.ConditionSlotsAssigned),
				withCondition(condition(valkeyv1alpha1.ConditionReady, metav1.ConditionFalse, valkeyv1alpha1.ReasonMissingShards, "Waiting for all shards to be created", statusTestGeneration)),
				withCondition(condition(valkeyv1alpha1.ConditionProgressing, metav1.ConditionTrue, valkeyv1alpha1.ReasonReconciling, "Creating shards", statusTestGeneration)),
			),
			nodes:   shardNodes(2),
			message: "Waiting for all shards to be created",
		},
		{
			name: "forming: the step is on Progressing",
			vc: healthyCluster(3,
				withoutCondition(valkeyv1alpha1.ConditionSlotsAssigned),
				withCondition(condition(valkeyv1alpha1.ConditionReady, metav1.ConditionFalse, valkeyv1alpha1.ReasonReconciling, "Cluster is Reconciling", statusTestGeneration)),
				withCondition(condition(valkeyv1alpha1.ConditionProgressing, metav1.ConditionTrue, valkeyv1alpha1.ReasonAddingNodes, "Attaching replicas", statusTestGeneration)),
			),
			nodes:   shardNodes(3),
			message: "Attaching replicas",
		},
		{
			name: "forming: slots not assigned yet",
			vc: healthyCluster(3,
				withCondition(condition(valkeyv1alpha1.ConditionSlotsAssigned, metav1.ConditionFalse, valkeyv1alpha1.ReasonSlotsUnassigned, "Waiting for slots to be assigned", statusTestGeneration)),
				withCondition(condition(valkeyv1alpha1.ConditionReady, metav1.ConditionFalse, valkeyv1alpha1.ReasonReconciling, "Waiting for all slots to be assigned", statusTestGeneration)),
			),
			nodes:     shardNodes(3),
			message:   "Waiting for all slots to be assigned",
			available: controller.Unavailable,
			availWhy:  reasonSlotsUnassigned,
			availMsg:  "Some hash slots are not served by any primary",
		},
		{
			name: "latest generation not processed yet",
			vc: healthyCluster(3, func(vc *valkeyv1alpha1.ValkeyCluster) {
				vc.Generation = statusTestGeneration + 1
			}),
			nodes:     shardNodes(3),
			message:   "Waiting for the operator to apply the latest configuration",
			available: controller.Available,
		},
		{
			name: "rolling a spec change",
			vc: healthyCluster(3,
				withCondition(condition(valkeyv1alpha1.ConditionReady, metav1.ConditionFalse, valkeyv1alpha1.ReasonUpdatingNodes, "Updating ValkeyNodes", statusTestGeneration)),
				withCondition(condition(valkeyv1alpha1.ConditionProgressing, metav1.ConditionTrue, valkeyv1alpha1.ReasonUpdatingNodes, "Updating ValkeyNodes", statusTestGeneration)),
			),
			nodes:     shardNodes(3),
			message:   "Updating ValkeyNodes",
			available: controller.Available,
		},
		{
			name: "rolling a new image",
			vc: healthyCluster(1, func(vc *valkeyv1alpha1.ValkeyCluster) {
				vc.Spec.Image = "valkey/valkey:9.1.2"
			}, withCondition(condition(valkeyv1alpha1.ConditionReady, metav1.ConditionFalse, valkeyv1alpha1.ReasonUpdatingNodes, "Updating ValkeyNodes", statusTestGeneration))),
			nodes: []valkeyv1alpha1.ValkeyNode{
				shardNode(0, 0, primaryRole),
				shardNode(0, 1, "replica", func(n *valkeyv1alpha1.ValkeyNode) { n.Spec.Image = "valkey/valkey:9.1.2" }),
				shardNode(0, 2, "replica"),
			},
			message:    "Updating ValkeyNodes",
			available:  controller.Available,
			operations: []controller.Operation{{Type: corev1alpha1.ConditionUpgrading, Reason: reasonRollingOutImage, Message: "Rolling out image valkey/valkey:9.1.2 (1 of 3 nodes updated)"}},
		},
		{
			name: "operator ready, a node it updated is not caught up in the cache",
			vc:   healthyCluster(1),
			nodes: []valkeyv1alpha1.ValkeyNode{
				shardNode(0, 0, primaryRole),
				shardNode(0, 1, "replica", func(n *valkeyv1alpha1.ValkeyNode) { n.Generation = 3 }),
			},
			message:   "Waiting for the operator to update ValkeyNode cache-0-1",
			available: controller.Available,
		},
		{
			name: "operator ready, a node is not ready",
			vc:   healthyCluster(1),
			nodes: []valkeyv1alpha1.ValkeyNode{
				shardNode(0, 0, primaryRole),
				shardNode(0, 1, "replica", down),
			},
			message:   "Waiting for ValkeyNode cache-0-1 to become ready",
			available: controller.Available,
		},
		{
			name: "volumes growing after the pods rolled",
			vc: healthyCluster(1, func(vc *valkeyv1alpha1.ValkeyCluster) {
				vc.Spec.Persistence.Size = resource.MustParse("20Gi")
			}),
			nodes: []valkeyv1alpha1.ValkeyNode{
				shardNode(0, 0, primaryRole, func(n *valkeyv1alpha1.ValkeyNode) {
					n.Spec.Persistence.Size = resource.MustParse("20Gi")
				}, volume(metav1.ConditionFalse, valkeyv1alpha1.ValkeyNodeReasonPersistentVolumeClaimResizeInProgress, "PersistentVolumeClaim data-0-0 resize is in progress")),
				shardNode(0, 1, "replica", func(n *valkeyv1alpha1.ValkeyNode) {
					n.Spec.Persistence.Size = resource.MustParse("20Gi")
				}, volume(metav1.ConditionTrue, valkeyv1alpha1.ValkeyNodeReasonPersistentVolumeClaimSizeSatisfied, "satisfied")),
			},
			message:    "Waiting for the volumes of the Valkey nodes to grow to 20Gi (1 of 2 not resized yet)",
			available:  controller.Available,
			operations: []controller.Operation{{Type: corev1alpha1.ConditionStorageResizing, Reason: corev1alpha1.ReasonStorageExpansionTriggered, Message: "Resizing the volumes of the Valkey nodes to 20Gi (1 of 2 not resized yet)"}},
		},
		{
			name: "a volume cannot grow",
			vc: healthyCluster(1, func(vc *valkeyv1alpha1.ValkeyCluster) {
				vc.Spec.Persistence.Size = resource.MustParse("20Gi")
			}),
			nodes: []valkeyv1alpha1.ValkeyNode{
				shardNode(0, 0, primaryRole, func(n *valkeyv1alpha1.ValkeyNode) {
					n.Spec.Persistence.Size = resource.MustParse("20Gi")
				}, volume(metav1.ConditionFalse, valkeyv1alpha1.ValkeyNodeReasonPersistentVolumeClaimResizeInfeasible, "storageclass does not support volume expansion")),
			},
			message:    "Waiting for the volumes of the Valkey nodes to grow to 20Gi (1 of 1 not resized yet)",
			available:  controller.Available,
			problems:   []controller.Problem{{Reason: corev1alpha1.ReasonStorageResizeFailed, Message: "storageclass does not support volume expansion"}},
			operations: []controller.Operation{{Type: corev1alpha1.ConditionStorageResizing, Reason: corev1alpha1.ReasonStorageExpansionTriggered, Message: "Resizing the volumes of the Valkey nodes to 20Gi (1 of 1 not resized yet)"}},
		},
		{
			name: "a step of the operator's pass fails",
			vc: healthyCluster(1,
				withCondition(condition(valkeyv1alpha1.ConditionReady, metav1.ConditionFalse, valkeyv1alpha1.ReasonServiceError, "services \"valkey-cache\" is forbidden", statusTestGeneration)),
			),
			nodes:     shardNodes(1),
			message:   "The operator reports an error: services \"valkey-cache\" is forbidden",
			available: controller.Available,
			problems:  []controller.Problem{{Reason: valkeyv1alpha1.ReasonServiceError, Message: "services \"valkey-cache\" is forbidden"}},
		},
		{
			name: "a write conflict is a wait",
			vc: healthyCluster(1,
				withCondition(condition(valkeyv1alpha1.ConditionReady, metav1.ConditionFalse, valkeyv1alpha1.ReasonValkeyNodeError, conflict, statusTestGeneration)),
			),
			nodes:     shardNodes(1),
			message:   "Waiting for the operator to retry a conflicting write",
			available: controller.Available,
		},
		{
			name: "an error of an earlier generation is not a problem",
			vc: healthyCluster(1, func(vc *valkeyv1alpha1.ValkeyCluster) {
				vc.Generation = statusTestGeneration + 1
			}, withCondition(condition(valkeyv1alpha1.ConditionReady, metav1.ConditionFalse, valkeyv1alpha1.ReasonConfigMapError, "boom", statusTestGeneration))),
			nodes:     shardNodes(1),
			message:   "Waiting for the operator to apply the latest configuration",
			available: controller.Available,
		},
		{
			name: "the operator is locked out of a node",
			vc: healthyCluster(1,
				withCondition(condition(valkeyv1alpha1.ConditionReady, metav1.ConditionFalse, valkeyv1alpha1.ReasonOperatorAuthenticationFailed, "cache-0-1 rejects the _operator password", statusTestGeneration)),
				withCondition(condition(valkeyv1alpha1.ConditionDegraded, metav1.ConditionTrue, valkeyv1alpha1.ReasonOperatorAuthenticationFailed, "cache-0-1 rejects the _operator password", statusTestGeneration)),
			),
			nodes:     shardNodes(1),
			message:   "The operator reports an error: cache-0-1 rejects the _operator password",
			available: controller.Available,
			problems:  []controller.Problem{{Reason: valkeyv1alpha1.ReasonOperatorAuthenticationFailed, Message: "cache-0-1 rejects the _operator password"}},
		},
		{
			name: "ready but the ACL is not applied",
			vc: healthyCluster(1,
				withCondition(condition(valkeyv1alpha1.ConditionDegraded, metav1.ConditionTrue, valkeyv1alpha1.ReasonACLApplyFailed, "ACL not applied on cache-0-1", statusTestGeneration)),
			),
			nodes:     shardNodes(1),
			message:   "The operator reports the cluster degraded: ACL not applied on cache-0-1",
			available: controller.Available,
			problems:  []controller.Problem{{Reason: valkeyv1alpha1.ReasonACLApplyFailed, Message: "ACL not applied on cache-0-1"}},
		},
		{
			name: "an unschedulable pod is left to the runtime",
			vc: healthyCluster(3,
				withCondition(condition(valkeyv1alpha1.ConditionReady, metav1.ConditionFalse, valkeyv1alpha1.ReasonPodUnschedulable, "Pod valkey-cache-2-1-0 is unschedulable: 0/3 nodes are available", statusTestGeneration)),
				withCondition(condition(valkeyv1alpha1.ConditionDegraded, metav1.ConditionTrue, valkeyv1alpha1.ReasonPodUnschedulable, "Pod valkey-cache-2-1-0 is unschedulable: 0/3 nodes are available", statusTestGeneration)),
			),
			nodes:     shardNodes(3),
			message:   "Pod valkey-cache-2-1-0 is unschedulable: 0/3 nodes are available",
			available: controller.Available,
		},
		{
			name: "scale-in blocked",
			vc: healthyCluster(3,
				withCondition(condition(valkeyv1alpha1.ConditionReady, metav1.ConditionFalse, valkeyv1alpha1.ReasonScaleInBlocked, "no shard can take the slots", statusTestGeneration)),
			),
			nodes:     shardNodes(4),
			message:   "The operator reports an error: no shard can take the slots",
			available: controller.Available,
			problems:  []controller.Problem{{Reason: valkeyv1alpha1.ReasonScaleInBlocked, Message: "no shard can take the slots"}},
		},
		{
			name: "replication: the primary is down before the failover",
			vc: healthyCluster(1,
				withCondition(condition(valkeyv1alpha1.ConditionReady, metav1.ConditionFalse, valkeyv1alpha1.ReasonUpdatingNodes, "Updating ValkeyNodes", statusTestGeneration)),
			),
			nodes:     []valkeyv1alpha1.ValkeyNode{shardNode(0, 0, primaryRole, down), shardNode(0, 1, "replica")},
			message:   "Updating ValkeyNodes",
			available: controller.Unavailable,
			availWhy:  reasonNoPrimary,
			availMsg:  "No node reports the primary role",
		},
		{
			name: "replication: a replica took over",
			vc:   healthyCluster(1),
			nodes: []valkeyv1alpha1.ValkeyNode{
				shardNode(0, 0, "replica"),
				shardNode(0, 1, primaryRole),
			},
			converged: true,
			available: controller.Available,
		},
		{
			name:      "replication: no ValkeyNode at all",
			vc:        healthyCluster(1),
			available: controller.Unavailable,
			availWhy:  reasonNoPrimary,
			availMsg:  "No node reports the primary role",
			converged: true,
		},
		{
			name: "cluster: a shard lost its only node",
			vc: healthyCluster(3, func(vc *valkeyv1alpha1.ValkeyCluster) {
				vc.Spec.ReplicasPerShard = 0
			}),
			nodes: []valkeyv1alpha1.ValkeyNode{
				shardNode(0, 0, primaryRole),
				shardNode(1, 0, primaryRole, down),
				shardNode(2, 0, primaryRole, down),
			},
			message:   "Waiting for ValkeyNode cache-1-0 to become ready",
			available: controller.Unavailable,
			availWhy:  reasonNoPrimary,
			availMsg:  "No node of shards 1, 2 reports the primary role",
		},
		{
			name: "cluster: a shard added by a scale-out has no ValkeyNode yet",
			vc: healthyCluster(4,
				withCondition(condition(valkeyv1alpha1.ConditionReady, metav1.ConditionFalse, valkeyv1alpha1.ReasonUpdatingNodes, "Updating ValkeyNodes", statusTestGeneration)),
			),
			nodes:     shardNodes(3),
			message:   "Updating ValkeyNodes",
			available: controller.Available,
		},
		{
			name: "cluster: a shard a scale-in drains lost its primary",
			vc: healthyCluster(3,
				withCondition(condition(valkeyv1alpha1.ConditionReady, metav1.ConditionFalse, valkeyv1alpha1.ReasonReconciling, "Scaling in cluster", statusTestGeneration)),
			),
			nodes:     append(shardNodes(3), shardNode(3, 0, primaryRole, down), shardNode(3, 1, "replica")),
			message:   "Scaling in cluster",
			available: controller.Unavailable,
			availWhy:  reasonNoPrimary,
			availMsg:  "No node of shard 3 reports the primary role",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := observe(tt.vc, tt.nodes)
			assert.Equal(t, tt.converged, got.Converged, "converged")
			assert.Equal(t, tt.message, got.Message, "message")
			assert.Equal(t, tt.available, got.Available, "available")
			assert.Equal(t, tt.availWhy, got.AvailableReason, "available reason")
			assert.Equal(t, tt.availMsg, got.AvailableMessage, "available message")
			assert.Equal(t, tt.problems, got.Problems, "problems")
			assert.Equal(t, tt.operations, got.Operations, "operations")
		})
	}
}

func TestLiveNodes(t *testing.T) {
	t.Parallel()

	deleting := shardNode(1, 0, primaryRole, func(n *valkeyv1alpha1.ValkeyNode) {
		now := metav1.Now()
		n.DeletionTimestamp = &now
		n.Finalizers = []string{"valkey.io/persistent-volume-cleanup"}
	})
	got := liveNodes([]valkeyv1alpha1.ValkeyNode{shardNode(0, 1, "replica"), deleting, shardNode(0, 0, primaryRole)})

	require.Len(t, got, 2)
	assert.Equal(t, "cache-0-0", got[0].Name)
	assert.Equal(t, "cache-0-1", got[1].Name)
}

// statusContext returns a provider context over a fake cluster holding a
// replication Instance without TLS, the ValkeyCluster, the auth Secret and
// objs.
func statusContext(t *testing.T, vc *valkeyv1alpha1.ValkeyCluster, objs ...client.Object) *controller.Context {
	t.Helper()
	auth := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: authSecretName(testInstance), Namespace: testNamespace},
		Data:       map[string][]byte{authSecretKeyPassword: []byte("s3cret")},
	}
	if vc != nil {
		objs = append(objs, vc)
	}
	c, _ := newTestContext(t, newTestInstance(`{"tls":{"mode":"disabled"}}`, false), interceptor.Funcs{}, append(objs, auth)...)
	return c
}

func clusterNode(shard, index int, role string, changes ...func(*valkeyv1alpha1.ValkeyNode)) *valkeyv1alpha1.ValkeyNode {
	n := shardNode(shard, index, role, changes...)
	n.Labels[controller.ProviderLabel] = common.ProviderName
	return &n
}

func TestStatus(t *testing.T) {
	t.Parallel()

	t.Run("ValkeyCluster not created yet", func(t *testing.T) {
		t.Parallel()
		got, err := New(ScrapeConfig{}).Status(statusContext(t, nil))
		require.NoError(t, err)
		assert.Equal(t, controller.Progressing("Waiting for the ValkeyCluster object to be created"), got)
	})

	t.Run("converged publishes the connection details", func(t *testing.T) {
		t.Parallel()
		c := statusContext(t, healthyCluster(1), clusterNode(0, 0, primaryRole), clusterNode(0, 1, "replica"))
		got, err := New(ScrapeConfig{}).Status(c)
		require.NoError(t, err)
		assert.True(t, got.Converged)
		assert.Equal(t, controller.Available, got.Available)
		assert.Equal(t, "valkey-cache-primary.team-a.svc.cluster.local", got.ConnectionDetails.Host)
		assert.Equal(t, "s3cret", got.ConnectionDetails.Password)
	})

	t.Run("available while rolling publishes the connection details", func(t *testing.T) {
		t.Parallel()
		vc := healthyCluster(1, withCondition(condition(valkeyv1alpha1.ConditionReady, metav1.ConditionFalse, valkeyv1alpha1.ReasonUpdatingNodes, "Updating ValkeyNodes", statusTestGeneration)))
		c := statusContext(t, vc, clusterNode(0, 0, primaryRole), clusterNode(0, 1, "replica"))
		got, err := New(ScrapeConfig{}).Status(c)
		require.NoError(t, err)
		assert.False(t, got.Converged)
		assert.Equal(t, controller.Available, got.Available)
		assert.False(t, got.ConnectionDetails.IsEmpty())
	})

	t.Run("nodes of other clusters and nodes being deleted do not count", func(t *testing.T) {
		t.Parallel()
		other := clusterNode(0, 0, primaryRole, func(n *valkeyv1alpha1.ValkeyNode) {
			n.Name = "other-0-0"
			n.Labels[clusterLabel] = "other"
		})
		deleting := clusterNode(0, 1, primaryRole, func(n *valkeyv1alpha1.ValkeyNode) {
			now := metav1.Now()
			n.DeletionTimestamp = &now
			n.Finalizers = []string{"valkey.io/persistent-volume-cleanup"}
		})
		vc := healthyCluster(1, withCondition(condition(valkeyv1alpha1.ConditionReady, metav1.ConditionFalse, valkeyv1alpha1.ReasonUpdatingNodes, "Updating ValkeyNodes", statusTestGeneration)))
		c := statusContext(t, vc, other, deleting, clusterNode(0, 0, "replica"))
		got, err := New(ScrapeConfig{}).Status(c)
		require.NoError(t, err)
		assert.Equal(t, controller.Unavailable, got.Available)
		assert.Equal(t, reasonNoPrimary, got.AvailableReason)
		assert.True(t, got.ConnectionDetails.IsEmpty())
	})

	t.Run("connection details that cannot be built are an error", func(t *testing.T) {
		t.Parallel()
		c, _ := newTestContext(t, newTestInstance(`{"tls":{"mode":"disabled"}}`, false), interceptor.Funcs{},
			healthyCluster(1), clusterNode(0, 0, primaryRole))
		_, err := New(ScrapeConfig{}).Status(c)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "build connection details")
	})
}
