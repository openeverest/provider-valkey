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
	"testing"

	commonv1alpha1 "github.com/openeverest/openeverest/v2/api/common/v1alpha1"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	valkeyv1alpha1 "github.com/valkey-io/valkey-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/openeverest/provider-valkey/internal/common"
)

const (
	testInstance  = "cache"
	testNamespace = "team-a"
)

// testProviderSpec carries the version catalog the provider resolves engine
// versions from.
var testProviderSpec = corev1alpha1.ProviderSpec{
	Components: map[string]corev1alpha1.Component{
		common.ComponentEngine:     {Type: common.ComponentTypeValkey},
		common.ComponentMonitoring: {Type: common.ComponentTypeExporter},
	},
	ComponentTypes: map[string]corev1alpha1.ComponentType{
		common.ComponentTypeValkey: {
			DefaultVersion: "9.0.0",
			Versions: []corev1alpha1.ComponentVersion{
				{Version: "9.0.0", Image: "valkey/valkey:9.0.0"},
				{Version: "8.1.1", Image: "valkey/valkey:8.1.1"},
			},
		},
	},
	DefaultVersion: "9.0",
	Versions: []corev1alpha1.VersionBundle{
		{Name: "9.0", Components: map[string]string{common.ComponentEngine: "9.0.0"}},
		{Name: "8.1", Components: map[string]string{common.ComponentEngine: "8.1.1"}},
	},
}

// newTestInstance builds an Instance whose engine carries the given raw
// parameters (empty for none).
func newTestInstance(engineParameters string, monitoring bool) *corev1alpha1.Instance {
	engine := corev1alpha1.ComponentSpec{Type: common.ComponentTypeValkey}
	if engineParameters != "" {
		engine.Parameters = &runtime.RawExtension{Raw: []byte(engineParameters)}
	}
	components := map[string]corev1alpha1.ComponentSpec{common.ComponentEngine: engine}
	if monitoring {
		components[common.ComponentMonitoring] = corev1alpha1.ComponentSpec{Type: common.ComponentTypeExporter}
	}
	return &corev1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: testInstance, Namespace: testNamespace, UID: "uid"},
		Spec: corev1alpha1.InstanceSpec{
			ProviderRef: commonv1alpha1.ObjectRef{Name: common.ProviderName},
			Topology:    &corev1alpha1.TopologySpec{Type: common.TopologyReplication},
			Components:  components,
		},
	}
}

func newTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		corev1.AddToScheme, corev1alpha1.AddToScheme, valkeyv1alpha1.AddToScheme, monitoringv1.AddToScheme,
	} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	return scheme
}

// newTestContext returns a provider context over a fake cluster holding the
// Instance, the Provider and objs.
func newTestContext(t *testing.T, in *corev1alpha1.Instance, funcs interceptor.Funcs, objs ...client.Object) (*controller.Context, client.Client) {
	t.Helper()
	providerObj := &corev1alpha1.Provider{
		ObjectMeta: metav1.ObjectMeta{Name: common.ProviderName},
		Spec:       testProviderSpec,
	}
	cl := fake.NewClientBuilder().
		WithScheme(newTestScheme(t)).
		WithObjects(append([]client.Object{in, providerObj}, objs...)...).
		WithInterceptorFuncs(funcs).
		Build()
	return controller.NewContext(context.Background(), cl, in, common.ProviderName), cl
}

func getSecret(t *testing.T, cl client.Client, name string) *corev1.Secret {
	t.Helper()
	secret := &corev1.Secret{}
	if err := cl.Get(context.Background(), client.ObjectKey{Namespace: testNamespace, Name: name}, secret); err != nil {
		t.Fatalf("getting secret %q: %v", name, err)
	}
	return secret
}
