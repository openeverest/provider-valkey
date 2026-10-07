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
	commonv1alpha1 "github.com/openeverest/openeverest/v2/api/common/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	valkeyv1alpha1 "github.com/valkey-io/valkey-operator/api/v1alpha1"
)

// clusterPodLabel is set by the valkey-operator on every pod of a cluster.
// Each pod is its own single-replica workload, so this is the only label that
// selects them all.
const clusterPodLabel = "valkey.io/cluster"

// buildScheduling places the pods of the named cluster from the engine's
// scheduling policy. Unless the user brings their own affinity, the members of
// each shard are required to run on separate nodes.
func buildScheduling(clusterName string, policy *commonv1alpha1.SchedulingPolicy) *valkeyv1alpha1.SchedulingSpec {
	scheduling := &valkeyv1alpha1.SchedulingSpec{}
	if policy != nil {
		scheduling.Affinity = policy.Affinity
		scheduling.NodeSelector = policy.NodeSelector
		scheduling.Tolerations = policy.Tolerations
	}
	if policy == nil || policy.Affinity == nil {
		scheduling.Node = &valkeyv1alpha1.NodeScheduling{
			Spread: valkeyv1alpha1.NodeSpread{
				Shard: valkeyv1alpha1.SpreadConstraint{Mode: valkeyv1alpha1.SpreadRequired},
			},
		}
	}
	scheduling.TopologySpreadConstraints = controller.TopologySpreadConstraints(policy, map[string]string{
		clusterPodLabel: clusterName,
	})
	return scheduling
}
