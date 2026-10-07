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
	"testing"

	commonv1alpha1 "github.com/openeverest/openeverest/v2/api/common/v1alpha1"
	valkeyv1alpha1 "github.com/valkey-io/valkey-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestBuildScheduling(t *testing.T) {
	requiredShardSpread := &valkeyv1alpha1.NodeScheduling{
		Spread: valkeyv1alpha1.NodeSpread{
			Shard: valkeyv1alpha1.SpreadConstraint{Mode: valkeyv1alpha1.SpreadRequired},
		},
	}
	userAffinity := &corev1.Affinity{
		NodeAffinity: &corev1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{{
					MatchExpressions: []corev1.NodeSelectorRequirement{{
						Key: "pool", Operator: corev1.NodeSelectorOpIn, Values: []string{"db"},
					}},
				}},
			},
		},
	}
	ownSelector := &metav1.LabelSelector{MatchLabels: map[string]string{"app": "other"}}

	tests := []struct {
		name   string
		policy *commonv1alpha1.SchedulingPolicy
		want   *valkeyv1alpha1.SchedulingSpec
	}{
		{
			name: "no policy requires shard members on separate nodes",
			want: &valkeyv1alpha1.SchedulingSpec{Node: requiredShardSpread},
		},
		{
			name: "policy without affinity keeps the default and passes the rest through",
			policy: &commonv1alpha1.SchedulingPolicy{
				NodeSelector: map[string]string{"pool": "db"},
				Tolerations:  []corev1.Toleration{{Key: "dedicated", Operator: corev1.TolerationOpExists}},
			},
			want: &valkeyv1alpha1.SchedulingSpec{
				Node:         requiredShardSpread,
				NodeSelector: map[string]string{"pool": "db"},
				Tolerations:  []corev1.Toleration{{Key: "dedicated", Operator: corev1.TolerationOpExists}},
			},
		},
		{
			name:   "user affinity replaces the default",
			policy: &commonv1alpha1.SchedulingPolicy{Affinity: userAffinity},
			want:   &valkeyv1alpha1.SchedulingSpec{Affinity: userAffinity},
		},
		{
			name:   "empty affinity sets no constraints",
			policy: &commonv1alpha1.SchedulingPolicy{Affinity: &corev1.Affinity{}},
			want:   &valkeyv1alpha1.SchedulingSpec{Affinity: &corev1.Affinity{}},
		},
		{
			name: "spread constraints without a selector count the cluster's pods",
			policy: &commonv1alpha1.SchedulingPolicy{
				Affinity: &corev1.Affinity{},
				TopologySpreadConstraints: &[]corev1.TopologySpreadConstraint{
					{MaxSkew: 1, TopologyKey: corev1.LabelTopologyZone, WhenUnsatisfiable: corev1.ScheduleAnyway},
					{MaxSkew: 1, TopologyKey: corev1.LabelHostname, WhenUnsatisfiable: corev1.ScheduleAnyway, LabelSelector: ownSelector},
				},
			},
			want: &valkeyv1alpha1.SchedulingSpec{
				Affinity: &corev1.Affinity{},
				TopologySpreadConstraints: []corev1.TopologySpreadConstraint{
					{
						MaxSkew: 1, TopologyKey: corev1.LabelTopologyZone, WhenUnsatisfiable: corev1.ScheduleAnyway,
						LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{clusterPodLabel: "vk"}},
					},
					{MaxSkew: 1, TopologyKey: corev1.LabelHostname, WhenUnsatisfiable: corev1.ScheduleAnyway, LabelSelector: ownSelector},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildScheduling("vk", tt.policy)
			if !equality.Semantic.DeepEqual(got, tt.want) {
				t.Fatalf("buildScheduling() =\n%+v\nwant\n%+v", got, tt.want)
			}
		})
	}
}
