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

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	valkeyv1alpha1 "github.com/valkey-io/valkey-operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/openeverest/provider-valkey/internal/common"
)

func TestBuildClusterSpecPersistence(t *testing.T) {
	in := newTestInstance("", false)
	engine := in.Spec.Components[common.ComponentEngine]
	engine.Storage = &corev1alpha1.Storage{Size: resource.MustParse("10Gi"), StorageClass: ptr.To("fast")}
	in.Spec.Components[common.ComponentEngine] = engine
	c, _ := newTestContext(t, in, interceptor.Funcs{})

	spec, err := buildClusterSpec(c)
	if err != nil {
		t.Fatal(err)
	}
	want := valkeyv1alpha1.PersistenceSpec{
		Size:             resource.MustParse("10Gi"),
		StorageClassName: ptr.To("fast"),
		ReclaimPolicy:    valkeyv1alpha1.PersistenceReclaimPolicyDelete,
	}
	if spec.Persistence == nil || !spec.Persistence.Size.Equal(want.Size) ||
		*spec.Persistence.StorageClassName != *want.StorageClassName || spec.Persistence.ReclaimPolicy != want.ReclaimPolicy {
		t.Fatalf("persistence = %+v, want %+v", spec.Persistence, want)
	}
}
