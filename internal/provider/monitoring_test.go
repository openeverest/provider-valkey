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
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	valkeyv1alpha1 "github.com/valkey-io/valkey-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	commonv1alpha1 "github.com/openeverest/openeverest/v2/api/common/v1alpha1"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	monitoringv1alpha1 "github.com/openeverest/openeverest/v2/api/monitoring/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	"github.com/openeverest/openeverest/v2/provider-runtime/monitoring"

	"github.com/openeverest/provider-valkey/internal/common"
)

// testCluster is the live ValkeyCluster of the test Instance, its exporter
// set by enabled (nil leaves it to the operator, which runs it).
func testCluster(enabled *bool) *valkeyv1alpha1.ValkeyCluster {
	return &valkeyv1alpha1.ValkeyCluster{
		ObjectMeta: metav1.ObjectMeta{Name: testInstance, Namespace: testNamespace},
		Spec:       valkeyv1alpha1.ValkeyClusterSpec{Exporter: valkeyv1alpha1.ExporterSpec{Enabled: enabled}},
	}
}

// testNode is a ValkeyNode of the test Instance.
func testNode(name string, exporter, ready, observed bool) *valkeyv1alpha1.ValkeyNode {
	node := &valkeyv1alpha1.ValkeyNode{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: testNamespace, Generation: 2,
			Labels: map[string]string{clusterLabel: testInstance},
		},
		Spec:   valkeyv1alpha1.ValkeyNodeSpec{Exporter: valkeyv1alpha1.ExporterSpec{Enabled: ptr.To(exporter)}},
		Status: valkeyv1alpha1.ValkeyNodeStatus{Ready: ready, ObservedGeneration: 1},
	}
	if observed {
		node.Status.ObservedGeneration = 2
	}
	return node
}

func TestMonitoringSources(t *testing.T) {
	t.Parallel()
	endpoint := corev1alpha1.MetricsEndpoint{
		Component: common.ComponentEngine,
		Kind:      common.KindValkey,
		PodSelector: map[string]string{
			controller.InstanceLabel:  testInstance,
			controller.ComponentLabel: common.ComponentEngine,
		},
		Port:   intstr.FromString("metrics"),
		Path:   "/metrics",
		Scheme: "http",
	}
	tests := []struct {
		name string
		objs []client.Object
		want []corev1alpha1.MetricsEndpoint
	}{
		{name: "no cluster yet"},
		{
			name: "exporter off",
			objs: []client.Object{testCluster(ptr.To(false)), testNode("n0", false, true, true)},
		},
		{
			name: "creating: no node ready",
			objs: []client.Object{testCluster(ptr.To(true)), testNode("n0", true, false, true)},
		},
		{
			name: "exporter just turned on: the node has not picked it up",
			objs: []client.Object{testCluster(ptr.To(true)), testNode("n0", false, true, true)},
		},
		{
			name: "updating: the ready node has not observed its spec",
			objs: []client.Object{testCluster(ptr.To(true)), testNode("n0", true, true, false)},
		},
		{
			name: "ready",
			objs: []client.Object{testCluster(ptr.To(true)), testNode("n0", true, true, true)},
			want: []corev1alpha1.MetricsEndpoint{endpoint},
		},
		{
			name: "rolling: one node still serves",
			objs: []client.Object{
				testCluster(ptr.To(true)), testNode("n0", true, false, false), testNode("n1", true, true, true),
			},
			want: []corev1alpha1.MetricsEndpoint{endpoint},
		},
		{
			name: "exporter left to the operator default",
			objs: []client.Object{testCluster(nil), testNode("n0", true, true, true)},
			want: []corev1alpha1.MetricsEndpoint{endpoint},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c, _ := newTestContext(t, newTestInstance("", false), interceptor.Funcs{}, tt.objs...)
			got, err := New().MonitoringSources(c)
			require.NoError(t, err)
			// Empty, not nil: nil would clear sources the runtime published.
			require.NotNil(t, got)
			assert.Equal(t, tt.want, got.Metrics)
		})
	}
}

// A read error keeps the published sources: the runtime keeps them when
// MonitoringSources fails.
func TestMonitoringSourcesReadErrors(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	for name, funcs := range map[string]interceptor.Funcs{
		"ValkeyCluster": {Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*valkeyv1alpha1.ValkeyCluster); ok {
				return boom
			}
			return cl.Get(ctx, key, obj, opts...)
		}},
		"ValkeyNodes": {List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if _, ok := list.(*valkeyv1alpha1.ValkeyNodeList); ok {
				return boom
			}
			return cl.List(ctx, list, opts...)
		}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c, _ := newTestContext(t, newTestInstance("", false), funcs, testCluster(ptr.To(true)), testNode("n0", true, true, true))
			got, err := New().MonitoringSources(c)
			require.ErrorIs(t, err, boom)
			assert.Nil(t, got)
		})
	}
}

// withDestination lists a destination named after the entry on the Instance.
func withDestination(in *corev1alpha1.Instance, entry string) *corev1alpha1.Instance {
	if in.Spec.Monitoring == nil {
		in.Spec.Monitoring = &corev1alpha1.InstanceMonitoringSpec{}
	}
	in.Spec.Monitoring.Destinations = append(in.Spec.Monitoring.Destinations, corev1alpha1.InstanceMonitoringDestination{
		Name:           entry,
		DestinationRef: commonv1alpha1.MonitoringDestinationRef{Name: entry},
	})
	return in
}

// metricsBinding is core's ExtensionManaged binding of the entry, of a class
// that does or does not require metrics.
func metricsBinding(entry string, requiresMetrics bool) []client.Object {
	class := &monitoringv1alpha1.MonitoringClass{
		ObjectMeta: metav1.ObjectMeta{Name: entry},
		Spec: monitoringv1alpha1.MonitoringClassSpec{
			ControllerName: "test.openeverest.io/" + entry,
			ExtensionManaged: &monitoringv1alpha1.ExtensionManagedFulfilment{
				Requires: monitoringv1alpha1.SourceRequirements{Metrics: requiresMetrics},
			},
		},
	}
	binding := &monitoringv1alpha1.MonitoringBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      monitoring.BindingName(testInstance, entry),
			Namespace: testNamespace,
			Labels:    monitoring.BindingLabels(testInstance, entry, entry, entry),
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "core.openeverest.io/v1alpha1", Kind: "Instance", Name: testInstance, UID: "uid", Controller: ptr.To(true),
			}},
		},
		Spec: monitoringv1alpha1.MonitoringBindingSpec{
			InstanceRef:    commonv1alpha1.ObjectRef{Name: testInstance},
			DestinationRef: commonv1alpha1.MonitoringDestinationRef{Kind: "MonitoringDestination", Name: entry},
			ClassRef:       &commonv1alpha1.ObjectRef{Name: entry},
			ExecutionMode:  corev1alpha1.MonitoringExecutionModeExtensionManaged,
		},
	}
	return []client.Object{class, binding}
}

func TestBuildExporterSpec(t *testing.T) {
	t.Parallel()
	exporterSpec := testProviderSpec.DeepCopy()
	exporterSpec.ComponentTypes[common.ComponentTypeExporter] = corev1alpha1.ComponentType{
		DefaultVersion: "1.80.0",
		Versions: []corev1alpha1.ComponentVersion{
			{Version: "1.80.0", Image: "oliver006/redis_exporter:v1.80.0"},
			{Version: "1.70.0", Image: "oliver006/redis_exporter:v1.70.0"},
		},
	}
	for i := range exporterSpec.Versions {
		exporterSpec.Versions[i].Components[common.ComponentMonitoring] = "1.80.0"
	}
	exporterSpec.Versions[1].Components[common.ComponentMonitoring] = "1.70.0"

	limits := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")}
	withComponent := func(version string) *corev1alpha1.Instance {
		in := newTestInstance("", true)
		monitoringComponent := in.Spec.Components[common.ComponentMonitoring]
		monitoringComponent.Version = version
		monitoringComponent.Resources = &corev1.ResourceRequirements{Limits: limits}
		in.Spec.Components[common.ComponentMonitoring] = monitoringComponent
		return in
	}
	onBundle := func(in *corev1alpha1.Instance, bundle string) *corev1alpha1.Instance {
		in.Spec.Version = bundle
		return in
	}

	tests := []struct {
		name          string
		in            *corev1alpha1.Instance
		objs          []client.Object
		wantEnabled   bool
		wantImage     string
		wantResources bool
	}{
		{
			name: "monitoring component", in: withComponent("1.70.0"),
			wantEnabled: true, wantImage: "oliver006/redis_exporter:v1.70.0", wantResources: true,
		},
		{name: "nothing asks for it", in: newTestInstance("", false)},
		{
			name: "a bound class requires metrics", in: withDestination(newTestInstance("", false), "prom"),
			objs:        metricsBinding("prom", true),
			wantEnabled: true, wantImage: "oliver006/redis_exporter:v1.80.0",
		},
		{
			name: "the exporter of the Instance's version bundle", in: onBundle(withDestination(newTestInstance("", false), "prom"), "8.1"),
			objs:        metricsBinding("prom", true),
			wantEnabled: true, wantImage: "oliver006/redis_exporter:v1.70.0",
		},
		{
			name: "bound classes do not require metrics", in: withDestination(newTestInstance("", false), "conn"),
			objs: append(metricsBinding("conn", false), testCluster(ptr.To(true))),
		},
		{
			name: "unresolved keeps a running exporter", in: withDestination(newTestInstance("", false), "pending"),
			objs:        []client.Object{testCluster(ptr.To(true))},
			wantEnabled: true, wantImage: "oliver006/redis_exporter:v1.80.0",
		},
		{
			name: "unresolved does not start a stopped exporter", in: withDestination(newTestInstance("", false), "pending"),
			objs: []client.Object{testCluster(ptr.To(false))},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			provider := &corev1alpha1.Provider{ObjectMeta: metav1.ObjectMeta{Name: common.ProviderName}, Spec: *exporterSpec}
			cl := newFakeClient(t, append([]client.Object{tt.in, provider}, tt.objs...)...)
			c := controller.NewContext(context.Background(), cl, tt.in, common.ProviderName)

			got, err := buildExporterSpec(c)
			require.NoError(t, err)
			assert.Equal(t, ptr.To(tt.wantEnabled), got.Enabled)
			assert.Equal(t, tt.wantImage, got.Image)
			if tt.wantResources {
				assert.Equal(t, limits, got.Resources.Limits)
			} else {
				assert.Empty(t, got.Resources.Limits)
			}
		})
	}
}

func TestDeleteLegacyScrapes(t *testing.T) {
	t.Parallel()
	podMonitor := schema.GroupKind{Group: "monitoring.coreos.com", Kind: "PodMonitor"}
	vmPodScrape := schema.GroupKind{Group: "operator.victoriametrics.com", Kind: "VMPodScrape"}

	type deleted struct {
		kind schema.GroupKind
		name string
	}
	run := func(t *testing.T, fail func(schema.GroupKind) error) ([]deleted, error) {
		t.Helper()
		var got []deleted
		funcs := interceptor.Funcs{Delete: func(_ context.Context, _ client.WithWatch, obj client.Object, _ ...client.DeleteOption) error {
			gk := obj.GetObjectKind().GroupVersionKind().GroupKind()
			assert.Equal(t, testNamespace, obj.GetNamespace())
			got = append(got, deleted{gk, obj.GetName()})
			return fail(gk)
		}}
		c, _ := newTestContext(t, newTestInstance("", false), funcs)
		return got, deleteLegacyScrapes(c)
	}

	t.Run("deleted", func(t *testing.T) {
		t.Parallel()
		got, err := run(t, func(schema.GroupKind) error { return nil })
		require.NoError(t, err)
		assert.ElementsMatch(t, []deleted{
			{podMonitor, testInstance + "-metrics"},
			{vmPodScrape, testInstance + "-metrics"},
			{podMonitor, testInstance + "-prometheus"},
			{vmPodScrape, testInstance + "-prometheus"},
			{vmPodScrape, testInstance + "-victoriametrics"},
		}, got)
	})
	t.Run("no CRDs", func(t *testing.T) {
		t.Parallel()
		got, err := run(t, func(gk schema.GroupKind) error { return &meta.NoKindMatchError{GroupKind: gk} })
		require.NoError(t, err)
		assert.Len(t, got, 5)
	})
	t.Run("API error", func(t *testing.T) {
		t.Parallel()
		boom := errors.New("boom")
		_, err := run(t, func(schema.GroupKind) error { return boom })
		require.ErrorIs(t, err, boom)
	})
}

// The generated Provider declares the valkey kind and that the engine's
// metrics come from an exporter, so core matches metrics classes to it and
// reports binding one as a rolling restart.
func TestProviderDeclaresMonitoringContract(t *testing.T) {
	t.Parallel()
	spec := generatedProviderSpec(t)
	assert.Equal(t, common.KindValkey, spec.ComponentTypes[common.ComponentTypeValkey].Kind)
	assert.Empty(t, spec.ComponentTypes[common.ComponentTypeExporter].Kind)
	require.NotNil(t, spec.Monitoring)
	assert.Equal(t, map[string]corev1alpha1.MonitoringComponentType{
		common.ComponentTypeValkey: {Metrics: corev1alpha1.MetricsSourceExporter},
	}, spec.Monitoring.ComponentTypes)
	assert.Empty(t, spec.Monitoring.Integrations)
}
