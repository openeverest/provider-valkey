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
	"strings"
	"testing"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	valkeyv1alpha1 "github.com/valkey-io/valkey-operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/openeverest/provider-valkey/internal/common"
)

func newStorageInstance(engineParameters string) *corev1alpha1.Instance {
	in := newTestInstance(engineParameters, false)
	engine := in.Spec.Components[common.ComponentEngine]
	engine.Storage = &corev1alpha1.Storage{Size: resource.MustParse("10Gi"), StorageClass: ptr.To("fast")}
	in.Spec.Components[common.ComponentEngine] = engine
	return in
}

func TestBuildClusterSpecPersistence(t *testing.T) {
	tests := []struct {
		name   string
		params string
		want   valkeyv1alpha1.PersistenceReclaimPolicy
	}{
		{name: "volumes are deleted by default", want: valkeyv1alpha1.PersistenceReclaimPolicyDelete},
		{name: "Delete", params: `{"volumeReclaimPolicy":"Delete"}`, want: valkeyv1alpha1.PersistenceReclaimPolicyDelete},
		{name: "Retain", params: `{"volumeReclaimPolicy":"Retain"}`, want: valkeyv1alpha1.PersistenceReclaimPolicyRetain},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newTestContext(t, newStorageInstance(tt.params), interceptor.Funcs{})
			spec, err := buildClusterSpec(c)
			if err != nil {
				t.Fatal(err)
			}
			p := spec.Persistence
			if p == nil || !p.Size.Equal(resource.MustParse("10Gi")) || p.StorageClassName == nil || *p.StorageClassName != "fast" {
				t.Fatalf("persistence = %+v, want 10Gi on storage class fast", p)
			}
			if p.ReclaimPolicy != tt.want {
				t.Fatalf("reclaimPolicy = %q, want %q", p.ReclaimPolicy, tt.want)
			}
		})
	}
}

func TestValidateVolumeReclaimPolicy(t *testing.T) {
	tests := []struct {
		params  string
		wantErr string
	}{
		{params: ""},
		{params: `{"volumeReclaimPolicy":"Delete"}`},
		{params: `{"volumeReclaimPolicy":"Retain"}`},
		{params: `{"volumeReclaimPolicy":"retain"}`, wantErr: "must be Delete or Retain"},
	}
	for _, tt := range tests {
		t.Run(tt.params, func(t *testing.T) {
			c, _ := newTestContext(t, newStorageInstance(tt.params), interceptor.Funcs{})
			err := (&Provider{}).Validate(c)
			if tt.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}
