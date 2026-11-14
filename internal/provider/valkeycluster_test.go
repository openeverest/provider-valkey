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
	"encoding/json"
	"testing"

	valkeyv1alpha1 "github.com/valkey-io/valkey-operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/equality"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/openeverest/provider-valkey/internal/common"
)

func TestBuildClusterSpecReplicasPerShard(t *testing.T) {
	tests := []struct {
		name     string
		replicas *int32
		want     int32
	}{
		{name: "unset", want: 0},
		{name: "none", replicas: new(int32(0)), want: 0},
		{name: "two", replicas: new(int32(2)), want: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := newTestInstance("", false)
			engine := in.Spec.Components[common.ComponentEngine]
			engine.Replicas = tt.replicas
			in.Spec.Components[common.ComponentEngine] = engine
			c, _ := newTestContext(t, in, interceptor.Funcs{})

			spec, err := buildClusterSpec(c)
			if err != nil {
				t.Fatalf("buildClusterSpec: %v", err)
			}
			if spec.ReplicasPerShard != tt.want {
				t.Errorf("replicasPerShard = %d, want %d", spec.ReplicasPerShard, tt.want)
			}
			assertDeprecatedFieldUnset(t, spec, "replicas")
		})
	}
}

func TestBuildClusterSpecDefaultUser(t *testing.T) {
	c, _ := newTestContext(t, newTestInstance("", false), interceptor.Funcs{})

	spec, err := buildClusterSpec(c)
	if err != nil {
		t.Fatalf("buildClusterSpec: %v", err)
	}
	assertDeprecatedFieldUnset(t, spec, "users")
	want := &valkeyv1alpha1.ACLSpec{Users: []valkeyv1alpha1.ACLUserSpec{{
		Name:  "default",
		State: valkeyv1alpha1.ACLUserStateEnabled,
		Password: &valkeyv1alpha1.ACLUserPassword{
			Type:   valkeyv1alpha1.ACLPasswordTypeSecret,
			Secret: &valkeyv1alpha1.PasswordSecretSpec{Name: testInstance + "-auth", Keys: []string{"default"}},
		},
		ACLPermissions: valkeyv1alpha1.ACLPermissions{RawAcl: "+@all ~* &*"},
	}}}
	if !equality.Semantic.DeepEqual(spec.ACL, want) {
		t.Errorf("acl = %+v, want %+v", spec.ACL, want)
	}
}

// assertDeprecatedFieldUnset checks that the serialized spec leaves out a
// deprecated field: valkey-operator rejects a spec that sets it alongside its
// replacement.
func assertDeprecatedFieldUnset(t *testing.T, spec valkeyv1alpha1.ValkeyClusterSpec, field string) {
	t.Helper()
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("unmarshal spec: %v", err)
	}
	if value, ok := fields[field]; ok {
		t.Errorf("deprecated %s = %s, want unset", field, value)
	}
}
