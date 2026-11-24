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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

// instanceParameters sets the raw spec.parameters.
func instanceParameters(raw string) func(*corev1alpha1.Instance) {
	return func(in *corev1alpha1.Instance) {
		in.Spec.Parameters = &runtime.RawExtension{Raw: []byte(raw)}
	}
}

// monitoringParameters adds the monitoring component with the raw parameters.
func monitoringParameters(raw string) func(*corev1alpha1.Instance) {
	return func(in *corev1alpha1.Instance) {
		in.Spec.Components[common.ComponentMonitoring] = corev1alpha1.ComponentSpec{
			Parameters: &runtime.RawExtension{Raw: []byte(raw)},
		}
	}
}

// TestValidate runs the checks the runtime runs before every Sync: the
// schemas, supportedFields and catalog the provider publishes, then Validate.
func TestValidate(t *testing.T) {
	t.Parallel()
	v := validationtest.New(t, New(), providerSpecPath)

	tests := []struct {
		name    string
		fixture string
		mutate  func(*corev1alpha1.Instance)
		// old turns the fixture into the stored Instance of an update; nil validates a create.
		old  func(*corev1alpha1.Instance)
		want []string
		// warns is whether Validate returns warnings.
		warns bool
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
			mutate:  instanceParameters(`{"tls":{"clientAuth":"sometimes"}}`),
			want:    []string{"spec.parameters.tls.clientAuth"},
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
			mutate:  instanceParameters(`{"tls":{"mode":"disabled","clientAuth":"optional"}}`),
		},
		{
			name:    "required client auth needs TLS",
			fixture: replicationFixture,
			mutate:  instanceParameters(`{"tls":{"mode":"disabled","clientAuth":"required"}}`),
			want:    []string{"spec.parameters.tls.clientAuth"},
		},
		{
			name:    "certificate user needs TLS",
			fixture: replicationFixture,
			mutate:  instanceParameters(`{"tls":{"mode":"disabled","certificateUser":"cn"}}`),
			want:    []string{"spec.parameters.tls.certificateUser"},
		},
		{
			name:    "certificate user is pointless with client auth disabled",
			fixture: replicationFixture,
			mutate:  instanceParameters(`{"tls":{"clientAuth":"disabled","certificateUser":"cn"}}`),
			want:    []string{"spec.parameters.tls.certificateUser"},
		},
		{
			name:    "certificate user on the default 9.0 bundle",
			fixture: replicationFixture,
			mutate:  instanceParameters(`{"tls":{"clientAuth":"required","certificateUser":"cn"}}`),
		},
		{
			name:    "certificate user needs Valkey 9.0",
			fixture: replicationFixture,
			mutate: func(in *corev1alpha1.Instance) {
				in.Spec.Version = "8.1"
				instanceParameters(`{"tls":{"certificateUser":"cn"}}`)(in)
			},
			want: []string{"spec.parameters.tls.certificateUser"},
		},
		{
			name:    "certificate user on a custom image is left to the operator",
			fixture: replicationFixture,
			mutate: func(in *corev1alpha1.Instance) {
				// The topologies' supportedFields do not offer image.
				in.Spec.Topology = nil
				in.Spec.Version = "8.1"
				instanceParameters(`{"tls":{"certificateUser":"cn"}}`)(in)
				component(common.ComponentEngine, func(c *corev1alpha1.ComponentSpec) {
					c.Image = "registry.example/valkey:custom"
				})(in)
			},
		},
		{
			name:    "running instance keeps requiring client certificates",
			fixture: replicationFixture,
			mutate:  instanceParameters(`{"tls":{"clientAuth":"required","certificateUser":"cn"}}`),
			old: func(in *corev1alpha1.Instance) {
				instanceParameters(`{"tls":{"clientAuth":"required"}}`)(in)
				in.Status.ObservedGeneration = 1
				in.Status.Phase = corev1alpha1.InstancePhaseProvisioned
			},
		},
		{
			name:    "running instance cannot stop requiring client certificates",
			fixture: replicationFixture,
			mutate:  instanceParameters(`{"tls":{"clientAuth":"optional"}}`),
			old: func(in *corev1alpha1.Instance) {
				instanceParameters(`{"tls":{"clientAuth":"required"}}`)(in)
				in.Status.ObservedGeneration = 1
				in.Status.Phase = corev1alpha1.InstancePhaseProvisioned
			},
			want: []string{"spec.parameters.tls.clientAuth"},
		},
		{
			// The provider has created the cluster, whose nodes require client
			// certificates, although it has not been available yet.
			name:    "created instance that has never been available cannot stop requiring client certificates",
			fixture: replicationFixture,
			mutate:  instanceParameters(`{"tls":{"clientAuth":"optional"}}`),
			old: func(in *corev1alpha1.Instance) {
				instanceParameters(`{"tls":{"clientAuth":"required"}}`)(in)
				in.Status.ObservedGeneration = 1
				in.Status.Phase = corev1alpha1.InstancePhasePending
			},
			want: []string{"spec.parameters.tls.clientAuth"},
		},
		{
			name:    "instance no provider has reconciled can stop requiring client certificates",
			fixture: replicationFixture,
			mutate:  instanceParameters(`{"tls":{"clientAuth":"optional"}}`),
			old: func(in *corev1alpha1.Instance) {
				instanceParameters(`{"tls":{"clientAuth":"required"}}`)(in)
				in.Status.ObservedGeneration = -1
				in.Status.Phase = corev1alpha1.InstancePhasePending
			},
		},
		{
			name:    "running instance can start requiring client certificates",
			fixture: replicationFixture,
			mutate:  instanceParameters(`{"tls":{"clientAuth":"required"}}`),
			old: func(in *corev1alpha1.Instance) {
				in.Status.ObservedGeneration = 1
				in.Status.Phase = corev1alpha1.InstancePhaseProvisioned
			},
		},
		{
			name:    "TLS disabled at creation",
			fixture: replicationFixture,
			mutate:  instanceParameters(`{"tls":{"mode":"disabled"}}`),
		},
		{
			name:    "TLS cannot be turned off on a created instance",
			fixture: replicationFixture,
			mutate:  instanceParameters(`{"tls":{"mode":"disabled"}}`),
			old:     func(in *corev1alpha1.Instance) { in.Status.ObservedGeneration = 1 },
			want:    []string{"spec.parameters.tls.mode"},
		},
		{
			name:    "TLS cannot be turned on on a created instance",
			fixture: replicationFixture,
			old: func(in *corev1alpha1.Instance) {
				instanceParameters(`{"tls":{"mode":"disabled"}}`)(in)
				in.Status.ObservedGeneration = 1
			},
			want: []string{"spec.parameters.tls.mode"},
		},
		{
			// Its mode is checked against the ValkeyCluster instead.
			name:    "instance of an earlier release moves TLS to spec.parameters",
			fixture: replicationFixture,
			mutate:  instanceParameters(`{"tls":{"mode":"disabled"}}`),
			old: func(in *corev1alpha1.Instance) {
				engineParameters(`{"tls":{"mode":"disabled"}}`)(in)
				in.Status.ObservedGeneration = 1
			},
		},
		{
			name:    "a rejected instance can turn TLS on",
			fixture: replicationFixture,
			old: func(in *corev1alpha1.Instance) {
				instanceParameters(`{"tls":{"mode":"disabled","certificateUser":"cn"}}`)(in)
				in.Status.ObservedGeneration = 1
				in.Status.Conditions = []metav1.Condition{{
					Type: corev1alpha1.ConditionReady, Status: metav1.ConditionFalse, Reason: corev1alpha1.ReasonValidationFailed,
				}}
			},
		},
		{
			name:    "the certificate source cannot change on a created instance",
			fixture: replicationFixture,
			mutate:  instanceParameters(`{"tls":{"issuer":{"name":"company-ca","kind":"ClusterIssuer"}}}`),
			old:     func(in *corev1alpha1.Instance) { in.Status.ObservedGeneration = 1 },
			want:    []string{"spec.parameters.tls"},
			warns:   true,
		},
		{
			name:    "the issuer cannot change on a created instance",
			fixture: replicationFixture,
			mutate:  instanceParameters(`{"tls":{"issuer":{"name":"company-ca","kind":"ClusterIssuer"}}}`),
			old: func(in *corev1alpha1.Instance) {
				instanceParameters(`{"tls":{"issuer":{"name":"company-ca"}}}`)(in)
				in.Status.ObservedGeneration = 1
			},
			want:  []string{"spec.parameters.tls"},
			warns: true,
		},
		{
			name:    "additional names can change on a created instance",
			fixture: replicationFixture,
			mutate:  instanceParameters(`{"tls":{"additionalNames":["cache.example.com"]}}`),
			old:     func(in *corev1alpha1.Instance) { in.Status.ObservedGeneration = 1 },
		},
		{
			name:    "issuer and additional names",
			fixture: replicationFixture,
			mutate: instanceParameters(
				`{"tls":{"issuer":{"name":"company-ca","kind":"ClusterIssuer"},"additionalNames":["*.cache.example.com"]}}`),
			warns: true,
		},
		{
			name:    "issuer on Valkey 9.1, which reloads renewed certificates",
			fixture: replicationFixture,
			mutate: func(in *corev1alpha1.Instance) {
				in.Spec.Version = "9.1"
				instanceParameters(`{"tls":{"issuer":{"name":"company-ca"}}}`)(in)
			},
		},
		{
			name:    "certificate of the user on Valkey 9.0 configured to reload it",
			fixture: replicationFixture,
			mutate: func(in *corev1alpha1.Instance) {
				instanceParameters(`{"tls":{"certificateSecretName":"cache-cert"}}`)(in)
				engineParameters(`{"config":{"tls-auto-reload-interval":"60"}}`)(in)
			},
		},
		{
			name:    "certificate of the user on Valkey 9.0",
			fixture: replicationFixture,
			mutate:  instanceParameters(`{"tls":{"certificateSecretName":"cache-cert"}}`),
			warns:   true,
		},
		{
			name:    "both an issuer and a certificate Secret",
			fixture: replicationFixture,
			mutate:  instanceParameters(`{"tls":{"certificateSecretName":"cache-cert","issuer":{"name":"company-ca"}}}`),
			want:    []string{"spec.parameters.tls.issuer"},
			warns:   true,
		},
		{
			name:    "issuer of cert-manager of an unknown kind",
			fixture: replicationFixture,
			mutate:  instanceParameters(`{"tls":{"issuer":{"name":"company-ca","kind":"VaultIssuer"}}}`),
			want:    []string{"spec.parameters.tls.issuer.kind"},
			warns:   true,
		},
		{
			name:    "external issuer of any kind",
			fixture: replicationFixture,
			mutate: instanceParameters(
				`{"tls":{"issuer":{"name":"company-ca","kind":"AWSPCAClusterIssuer","group":"awspca.cert-manager.io"}}}`),
			warns: true,
		},
		{
			name:    "certificate Secret named like a Secret of the instance",
			fixture: replicationFixture,
			mutate: func(in *corev1alpha1.Instance) {
				instanceParameters(`{"tls":{"certificateSecretName":"` + in.Name + `-conn"}}`)(in)
			},
			want:  []string{"spec.parameters.tls.certificateSecretName"},
			warns: true,
		},
		{
			name:    "additional names with a certificate Secret",
			fixture: replicationFixture,
			mutate:  instanceParameters(`{"tls":{"certificateSecretName":"cache-cert","additionalNames":["cache.example.com"]}}`),
			want:    []string{"spec.parameters.tls.additionalNames"},
			warns:   true,
		},
		{
			name:    "IP address as an additional name",
			fixture: replicationFixture,
			mutate:  instanceParameters(`{"tls":{"additionalNames":["cache.example.com","10.0.0.1"]}}`),
			want:    []string{"spec.parameters.tls.additionalNames[1]"},
		},
		{
			name:    "certificate source with TLS disabled",
			fixture: replicationFixture,
			mutate:  instanceParameters(`{"tls":{"mode":"disabled","issuer":{"name":"company-ca"}}}`),
			want:    []string{"spec.parameters.tls"},
		},
		{
			name:    "name with a dot",
			fixture: replicationFixture,
			mutate:  func(in *corev1alpha1.Instance) { in.Name = "cache.v2" },
			want:    []string{"metadata.name"},
		},
		{
			name:    "existing instance with a dot in its name can still be updated",
			fixture: replicationFixture,
			mutate:  func(in *corev1alpha1.Instance) { in.Name = "cache.v2" },
			old: func(in *corev1alpha1.Instance) {
				in.Name = "cache.v2"
				in.Status.Phase = corev1alpha1.InstancePhaseProvisioned
			},
		},
		// The scrape parameters are deprecated and ignored, but earlier UIs
		// wrote them into Instances, so they are still accepted.
		{
			name:    "deprecated scrape parameters, both on",
			fixture: replicationFixture,
			mutate: monitoringParameters(
				`{"metrics":{"prometheus":{"enabled":true},"victoriaMetrics":{"enabled":true}}}`),
		},
		{
			name:    "deprecated scrape parameters, both off",
			fixture: clusterFixture,
			mutate: monitoringParameters(
				`{"metrics":{"prometheus":{"enabled":false},"victoriaMetrics":{"enabled":false}}}`),
		},
		{
			name:    "unknown monitoring parameter",
			fixture: replicationFixture,
			mutate:  monitoringParameters(`{"metrics":{"otel":{"enabled":true}}}`),
			want:    []string{"spec.components[monitoring].parameters.metrics.otel"},
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
			assert.Equal(t, tt.warns, len(res.Warnings) > 0, "warnings: %v", res.Warnings)
		})
	}
}

// TestValidateClusterNameLength checks that the name leaves room for the
// shard and node indexes in the names of the instance's StatefulSets.
func TestValidateClusterNameLength(t *testing.T) {
	t.Parallel()
	v := validationtest.New(t, New(), providerSpecPath)

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
