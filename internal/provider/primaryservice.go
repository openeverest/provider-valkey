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
	"context"
	"fmt"

	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	valkeyv1alpha1 "github.com/valkey-io/valkey-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/openeverest/provider-valkey/internal/common"
)

// Labels valkey-operator sets on ValkeyNodes and their pods.
const (
	clusterLabel   = "valkey.io/cluster"
	shardLabel     = "valkey.io/shard-index"
	nodeIndexLabel = "valkey.io/node-index"

	// primaryRole is the ValkeyNode status role of a shard's primary.
	primaryRole = "primary"
	// defaultPrimaryNodeIndex is the node the operator makes primary when it
	// creates a shard, used until a ValkeyNode reports its role.
	defaultPrimaryNodeIndex = "0"
)

// The replication topology runs a single-shard Valkey Cluster, so its replicas
// answer writes with MOVED. Clients that do not speak the cluster protocol
// (most Redis clients in their default mode, Envoy's rate limit service, ...)
// would see those errors on whichever connections land on a replica through
// the headless Service. The primary Service follows the primary instead.
func primaryServiceName(instance string) string {
	return headlessServicePrefix + instance + "-primary"
}

func primaryServiceFQDN(instance, namespace string) string {
	return fmt.Sprintf("%s.%s.svc.cluster.local", primaryServiceName(instance), namespace)
}

// connectionHost is the Service clients connect to: the primary Service for
// the replication topology, the headless Service for the cluster topology.
// TLS instances whose certificate predates the primary Service keep the
// headless Service, which their certificate covers.
func connectionHost(c *controller.Context) (string, error) {
	headless := fmt.Sprintf("%s%s.%s.svc.cluster.local", headlessServicePrefix, c.Name(), c.Namespace())
	if !usesPrimaryService(c) {
		return headless, nil
	}
	primary := primaryServiceFQDN(c.Name(), c.Namespace())
	if !tlsEnabled(c) {
		return primary, nil
	}
	covered, err := serverCertCovers(c, primary)
	if err != nil {
		return "", err
	}
	if covered {
		return primary, nil
	}
	return headless, nil
}

// usesPrimaryService reports whether clients connect through the primary
// Service rather than the headless one.
func usesPrimaryService(c *controller.Context) bool {
	return c.Instance().GetTopologyType() != common.TopologyCluster
}

// syncPrimaryService points the primary Service at the node the operator
// reports as primary.
func syncPrimaryService(c *controller.Context) error {
	if !usesPrimaryService(c) {
		return nil
	}
	nodeIndex, err := primaryNodeIndex(c)
	if err != nil {
		return err
	}
	return c.Apply(buildPrimaryService(c, nodeIndex))
}

func buildPrimaryService(c *controller.Context, nodeIndex string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: c.ObjectMeta(primaryServiceName(c.Name())),
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeClusterIP,
			Selector: map[string]string{
				clusterLabel:   c.Name(),
				shardLabel:     "0",
				nodeIndexLabel: nodeIndex,
			},
			Ports: []corev1.ServicePort{{
				Name:       "valkey",
				Port:       valkeyPortNumber,
				TargetPort: intstr.FromInt32(valkeyPortNumber),
			}},
		},
	}
}

// primaryNodeIndex returns the node index of the ValkeyNode reporting the
// primary role, or the operator's initial primary when none reports it yet.
func primaryNodeIndex(c *controller.Context) (string, error) {
	nodes := &valkeyv1alpha1.ValkeyNodeList{}
	if err := c.List(nodes, client.MatchingLabels{clusterLabel: c.Name()}); err != nil {
		return "", fmt.Errorf("listing ValkeyNodes: %w", err)
	}
	for _, node := range nodes.Items {
		if node.Status.Role == primaryRole && node.Labels[shardLabel] == "0" {
			if index := node.Labels[nodeIndexLabel]; index != "" {
				return index, nil
			}
		}
	}
	return defaultPrimaryNodeIndex, nil
}

// instanceForValkeyNode maps a ValkeyNode to the Instance of its cluster, so a
// failover re-points the primary Service.
func instanceForValkeyNode(_ context.Context, obj client.Object) []reconcile.Request {
	name := obj.GetLabels()[clusterLabel]
	if name == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: obj.GetNamespace(), Name: name}}}
}

var valkeyNodeHandler = handler.EnqueueRequestsFromMapFunc(instanceForValkeyNode)

// managedValkeyNode skips ValkeyNodes of clusters this provider does not own.
var managedValkeyNode = predicate.NewPredicateFuncs(func(obj client.Object) bool {
	return obj.GetLabels()[controller.ProviderLabel] == common.ProviderName
})
