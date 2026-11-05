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
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/runtime"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller/validationtest"

	"github.com/openeverest/provider-valkey/internal/common"
)

const (
	providerSpecPath   = "../../charts/provider-valkey/generated/provider-spec.yaml"
	replicationFixture = "testdata/validate/replication.yaml"
	clusterFixture     = "testdata/validate/cluster.yaml"
)

// component applies mutate to the named component of the Instance.
func component(name string, mutate func(*corev1alpha1.ComponentSpec)) func(*corev1alpha1.Instance) {
	return func(in *corev1alpha1.Instance) {
		c := in.Spec.Components[name]
		mutate(&c)
		in.Spec.Components[name] = c
	}
}

// engineParameters sets the engine's raw parameters.
func engineParameters(raw string) func(*corev1alpha1.Instance) {
	return component(common.ComponentEngine, func(c *corev1alpha1.ComponentSpec) {
		c.Parameters = &runtime.RawExtension{Raw: []byte(raw)}
	})
}

// TestValidate runs the checks the runtime runs before every Sync: the
// schemas, supportedFields and catalog the provider publishes, then Validate.
func TestValidate(t *testing.T) {
	t.Parallel()
	v := validationtest.New(t, New(ScrapeConfig{}), providerSpecPath)

	tests := []struct {
		name    string
		fixture string
		mutate  func(*corev1alpha1.Instance)
		// old turns the fixture into the stored Instance of an update; nil validates a create.
		old  func(*corev1alpha1.Instance)
		want []string
	}{
		{name: "valid replication", fixture: replicationFixture},
		{name: "valid cluster", fixture: clusterFixture},
		{
			name:    "no topology type",
			fixture: replicationFixture,
			mutate:  func(in *corev1alpha1.Instance) { in.Spec.Topology = nil },
		},
		{
			name:    "unknown version",
			fixture: replicationFixture,
			mutate:  func(in *corev1alpha1.Instance) { in.Spec.Version = "7.2" },
			want:    []string{"spec.version"},
		},
		{
			name:    "engine type other than valkey",
			fixture: replicationFixture,
			mutate:  component(common.ComponentEngine, func(c *corev1alpha1.ComponentSpec) { c.Type = "redis" }),
			want:    []string{"spec.components[engine].type"},
		},
		{
			name:    "fewer than three shards",
			fixture: clusterFixture,
			mutate: func(in *corev1alpha1.Instance) {
				in.Spec.Topology.Parameters = &runtime.RawExtension{Raw: []byte(`{"numShards":2}`)}
			},
			want: []string{"spec.topology.parameters.numShards"},
		},
		{
			name:    "unknown client authentication mode",
			fixture: replicationFixture,
			mutate:  engineParameters(`{"tls":{"clientAuth":"sometimes"}}`),
			want:    []string{"spec.components[engine].parameters.tls.clientAuth"},
		},
		{
			name:    "missing engine",
			fixture: clusterFixture,
			mutate:  func(in *corev1alpha1.Instance) { delete(in.Spec.Components, common.ComponentEngine) },
			want:    []string{"spec.components[engine]"},
		},
		{
			name:    "missing engine without a topology type",
			fixture: replicationFixture,
			mutate: func(in *corev1alpha1.Instance) {
				in.Spec.Topology = nil
				delete(in.Spec.Components, common.ComponentEngine)
			},
			want: []string{"spec.components[engine]"},
		},
		{
			name:    "negative engine replicas without a topology type",
			fixture: replicationFixture,
			mutate: func(in *corev1alpha1.Instance) {
				in.Spec.Topology = nil
				component(common.ComponentEngine, func(c *corev1alpha1.ComponentSpec) { c.Replicas = new(int32(-1)) })(in)
			},
			want: []string{"spec.components[engine].replicas"},
		},
		{
			name:    "optional client auth is accepted without TLS",
			fixture: replicationFixture,
			mutate:  engineParameters(`{"tls":{"mode":"disabled","clientAuth":"optional"}}`),
		},
		{
			name:    "required client auth needs TLS",
			fixture: replicationFixture,
			mutate:  engineParameters(`{"tls":{"mode":"disabled","clientAuth":"required"}}`),
			want:    []string{"spec.components[engine].parameters.tls.clientAuth"},
		},
		{
			name:    "certificate user needs TLS",
			fixture: replicationFixture,
			mutate:  engineParameters(`{"tls":{"mode":"disabled","certificateUser":"cn"}}`),
			want:    []string{"spec.components[engine].parameters.tls.certificateUser"},
		},
		{
			name:    "certificate user is pointless with client auth disabled",
			fixture: replicationFixture,
			mutate:  engineParameters(`{"tls":{"clientAuth":"disabled","certificateUser":"cn"}}`),
			want:    []string{"spec.components[engine].parameters.tls.certificateUser"},
		},
		{
			name:    "certificate user on the default 9.0 bundle",
			fixture: replicationFixture,
			mutate:  engineParameters(`{"tls":{"clientAuth":"required","certificateUser":"cn"}}`),
		},
		{
			name:    "certificate user needs Valkey 9.0",
			fixture: replicationFixture,
			mutate: func(in *corev1alpha1.Instance) {
				in.Spec.Version = "8.1"
				engineParameters(`{"tls":{"certificateUser":"cn"}}`)(in)
			},
			want: []string{"spec.components[engine].parameters.tls.certificateUser"},
		},
		{
			name:    "certificate user on a custom image is left to the operator",
			fixture: replicationFixture,
			mutate: func(in *corev1alpha1.Instance) {
				// The topologies' supportedFields do not offer image.
				in.Spec.Topology = nil
				in.Spec.Version = "8.1"
				engineParameters(`{"tls":{"certificateUser":"cn"}}`)(in)
				component(common.ComponentEngine, func(c *corev1alpha1.ComponentSpec) {
					c.Image = "registry.example/valkey:custom"
				})(in)
			},
		},
		{
			name:    "running instance keeps requiring client certificates",
			fixture: replicationFixture,
			mutate:  engineParameters(`{"tls":{"clientAuth":"required","certificateUser":"cn"}}`),
			old: func(in *corev1alpha1.Instance) {
				engineParameters(`{"tls":{"clientAuth":"required"}}`)(in)
				in.Status.Phase = corev1alpha1.InstancePhaseReady
			},
		},
		{
			name:    "running instance cannot stop requiring client certificates",
			fixture: replicationFixture,
			mutate:  engineParameters(`{"tls":{"clientAuth":"optional"}}`),
			old: func(in *corev1alpha1.Instance) {
				engineParameters(`{"tls":{"clientAuth":"required"}}`)(in)
				in.Status.Phase = corev1alpha1.InstancePhaseReady
			},
			want: []string{"spec.components[engine].parameters.tls.clientAuth"},
		},
		{
			name:    "pending instance can stop requiring client certificates",
			fixture: replicationFixture,
			mutate:  engineParameters(`{"tls":{"clientAuth":"optional"}}`),
			old: func(in *corev1alpha1.Instance) {
				engineParameters(`{"tls":{"clientAuth":"required"}}`)(in)
				in.Status.Phase = corev1alpha1.InstancePhasePending
			},
		},
		{
			name:    "running instance can start requiring client certificates",
			fixture: replicationFixture,
			mutate:  engineParameters(`{"tls":{"clientAuth":"required"}}`),
			old:     func(in *corev1alpha1.Instance) { in.Status.Phase = corev1alpha1.InstancePhaseReady },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			in := validationtest.Load(t, tt.fixture)
			if tt.mutate != nil {
				tt.mutate(in)
			}
			var old *corev1alpha1.Instance
			if tt.old != nil {
				old = validationtest.Load(t, tt.fixture)
				tt.old(old)
			}

			res := v.Run(in, old)

			var got []string
			for _, err := range res.Errors {
				got = append(got, err.Field)
			}
			assert.Equal(t, tt.want, got, "errors: %v", res.Errors)
		})
	}
}

// TestValidateClusterNameLength checks that the name leaves room for the
// shard and node indexes in the names of the instance's StatefulSets.
func TestValidateClusterNameLength(t *testing.T) {
	t.Parallel()
	v := validationtest.New(t, New(ScrapeConfig{}), providerSpecPath)

	tests := []struct {
		name     string
		nameLen  int
		fixture  string
		shards   int
		replicas *int32
		want     []string
	}{
		{name: "replication without replicas at the limit", nameLen: 41, fixture: replicationFixture},
		{name: "replication without replicas over the limit", nameLen: 42, fixture: replicationFixture, want: []string{"metadata.name"}},
		{name: "replication with 9 replicas at the limit", nameLen: 41, fixture: replicationFixture, replicas: new(int32(9))},
		{name: "a 10th replica takes a character", nameLen: 41, fixture: replicationFixture, replicas: new(int32(10)), want: []string{"metadata.name"}},
		{name: "10 shards at the limit", nameLen: 41, fixture: clusterFixture, shards: 10, replicas: new(int32(1))},
		{name: "11 shards take a character", nameLen: 41, fixture: clusterFixture, shards: 11, replicas: new(int32(1)), want: []string{"metadata.name"}},
		{name: "11 shards at the limit", nameLen: 40, fixture: clusterFixture, shards: 11, replicas: new(int32(1))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			in := validationtest.Load(t, tt.fixture)
			in.Name = strings.Repeat("v", tt.nameLen)
			if tt.shards > 0 {
				in.Spec.Topology.Parameters = &runtime.RawExtension{Raw: fmt.Appendf(nil, `{"numShards":%d}`, tt.shards)}
			}
			component(common.ComponentEngine, func(c *corev1alpha1.ComponentSpec) { c.Replicas = tt.replicas })(in)

			res := v.Run(in, nil)

			var got []string
			for _, err := range res.Errors {
				got = append(got, err.Field)
			}
			assert.Equal(t, tt.want, got, "errors: %v", res.Errors)
		})
	}
}
