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
	"maps"
	"slices"
	"strings"
	"testing"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	vmv1beta1 "github.com/openeverest/provider-valkey/internal/apis/victoriametrics/v1beta1"
	"github.com/openeverest/provider-valkey/internal/common"
)

const (
	prometheusOnly      = `{"metrics":{"prometheus":{"enabled":true}}}`
	victoriaMetricsOnly = `{"metrics":{"victoriaMetrics":{"enabled":true}}}`
	bothScrapes         = `{"metrics":{"prometheus":{"enabled":true},"victoriaMetrics":{"enabled":true}}}`
)

// newMonitoredInstance builds an Instance with the monitoring component
// carrying the given raw parameters (empty for none).
func newMonitoredInstance(monitoringParameters string) *corev1alpha1.Instance {
	in := newTestInstance("", true)
	if monitoringParameters != "" {
		monitoring := in.Spec.Components[common.ComponentMonitoring]
		monitoring.Parameters = &runtime.RawExtension{Raw: []byte(monitoringParameters)}
		in.Spec.Components[common.ComponentMonitoring] = monitoring
	}
	return in
}

// scrapeExists reports whether the scrape resource obj names exists.
func scrapeExists(t *testing.T, cl client.Client, obj client.Object, name string) bool {
	t.Helper()
	err := cl.Get(context.Background(), client.ObjectKey{Namespace: testNamespace, Name: name}, obj)
	if apierrors.IsNotFound(err) {
		return false
	}
	if err != nil {
		t.Fatalf("getting %s: %v", name, err)
	}
	return true
}

func TestScrapeConfigFromEnv(t *testing.T) {
	t.Setenv(podMonitorLabelsEnv, `{"release":"kube-prometheus-stack"}`)
	t.Setenv(vmPodScrapeLabelsEnv, `{"team":"dba"}`)
	cfg, err := ScrapeConfigFromEnv()
	if err != nil {
		t.Fatalf("ScrapeConfigFromEnv: %v", err)
	}
	if !maps.Equal(cfg.PodMonitorLabels, map[string]string{"release": "kube-prometheus-stack"}) ||
		!maps.Equal(cfg.VMPodScrapeLabels, map[string]string{"team": "dba"}) {
		t.Fatalf("ScrapeConfigFromEnv() = %+v", cfg)
	}

	for _, env := range []string{podMonitorLabelsEnv, vmPodScrapeLabelsEnv} {
		t.Run("invalid "+env, func(t *testing.T) {
			t.Setenv(env, "release=kps")
			if _, err := ScrapeConfigFromEnv(); err == nil || !strings.Contains(err.Error(), env) {
				t.Fatalf("ScrapeConfigFromEnv() error = %v, want one naming %s", err, env)
			}
		})
	}
}

func TestSyncScrapes(t *testing.T) {
	tests := []struct {
		name                string
		in                  *corev1alpha1.Instance
		wantPodMonitor      bool
		wantVMPodScrape     bool
		wantErrContainsText string
	}{
		{name: "no monitoring component", in: newTestInstance("", false)},
		{name: "monitoring without metrics parameters runs only the exporter", in: newMonitoredInstance("")},
		{name: "both scrapes off", in: newMonitoredInstance(`{"metrics":{"prometheus":{"enabled":false}}}`)},
		{name: "prometheus", in: newMonitoredInstance(prometheusOnly), wantPodMonitor: true},
		{name: "victoriametrics", in: newMonitoredInstance(victoriaMetricsOnly), wantVMPodScrape: true},
		{name: "both", in: newMonitoredInstance(bothScrapes), wantPodMonitor: true, wantVMPodScrape: true},
		{name: "invalid parameters", in: newMonitoredInstance(`{"metrics":"yes"}`), wantErrContainsText: "monitoring parameters"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, cl := newTestContext(t, tt.in, interceptor.Funcs{})
			err := syncScrapes(c, ScrapeConfig{})
			if tt.wantErrContainsText != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErrContainsText) {
					t.Fatalf("syncScrapes() error = %v, want one containing %q", err, tt.wantErrContainsText)
				}
				return
			}
			if err != nil {
				t.Fatalf("syncScrapes: %v", err)
			}
			if got := scrapeExists(t, cl, &monitoringv1.PodMonitor{}, podMonitorName(testInstance)); got != tt.wantPodMonitor {
				t.Errorf("PodMonitor exists = %v, want %v", got, tt.wantPodMonitor)
			}
			if got := scrapeExists(t, cl, &vmv1beta1.VMPodScrape{}, vmPodScrapeName(testInstance)); got != tt.wantVMPodScrape {
				t.Errorf("VMPodScrape exists = %v, want %v", got, tt.wantVMPodScrape)
			}
		})
	}
}

func TestScrapeResources(t *testing.T) {
	cfg := ScrapeConfig{
		PodMonitorLabels: map[string]string{
			"release":                    "kube-prometheus-stack",
			"app.kubernetes.io/instance": "spoofed",
		},
		VMPodScrapeLabels: map[string]string{"team": "dba", "app.kubernetes.io/managed-by": "spoofed"},
	}
	c, cl := newTestContext(t, newMonitoredInstance(bothScrapes), interceptor.Funcs{})
	if err := syncScrapes(c, cfg); err != nil {
		t.Fatalf("syncScrapes: %v", err)
	}

	pm := &monitoringv1.PodMonitor{}
	if !scrapeExists(t, cl, pm, podMonitorName(testInstance)) {
		t.Fatal("PodMonitor not created")
	}
	vs := &vmv1beta1.VMPodScrape{}
	if !scrapeExists(t, cl, vs, vmPodScrapeName(testInstance)) {
		t.Fatal("VMPodScrape not created")
	}

	// plugin-metrics finds the scrape resources by the managed labels, which
	// the configured ones must not override.
	for kind, labels := range map[string]map[string]string{"PodMonitor": pm.Labels, "VMPodScrape": vs.Labels} {
		if labels["app.kubernetes.io/instance"] != testInstance || labels["app.kubernetes.io/managed-by"] != "everest" {
			t.Errorf("%s managed labels = %v", kind, labels)
		}
	}
	if pm.Labels["release"] != "kube-prometheus-stack" || pm.Labels["team"] != "" {
		t.Errorf("PodMonitor labels = %v, want only the PodMonitor labels added", pm.Labels)
	}
	if vs.Labels["team"] != "dba" || vs.Labels["release"] != "" {
		t.Errorf("VMPodScrape labels = %v, want only the VMPodScrape labels added", vs.Labels)
	}

	wantSelector := map[string]string{clusterPodLabel: testInstance}
	wantTargetLabels := []string{controller.InstanceLabel, controller.ComponentLabel, shardIndexPodLabel, nodeIndexPodLabel}
	if !maps.Equal(pm.Spec.Selector.MatchLabels, wantSelector) || !maps.Equal(vs.Spec.Selector.MatchLabels, wantSelector) {
		t.Errorf("selectors = %v / %v, want %v", pm.Spec.Selector.MatchLabels, vs.Spec.Selector.MatchLabels, wantSelector)
	}
	if !slices.Equal(pm.Spec.PodTargetLabels, wantTargetLabels) || !slices.Equal(vs.Spec.PodTargetLabels, wantTargetLabels) {
		t.Errorf("podTargetLabels = %v / %v, want %v", pm.Spec.PodTargetLabels, vs.Spec.PodTargetLabels, wantTargetLabels)
	}
	if len(pm.Spec.PodMetricsEndpoints) != 1 || *pm.Spec.PodMetricsEndpoints[0].Port != exporterPortName ||
		pm.Spec.PodMetricsEndpoints[0].Path != exporterMetricsPath {
		t.Errorf("PodMonitor endpoints = %+v", pm.Spec.PodMetricsEndpoints)
	}
	if len(vs.Spec.PodMetricsEndpoints) != 1 || *vs.Spec.PodMetricsEndpoints[0].Port != exporterPortName ||
		vs.Spec.PodMetricsEndpoints[0].Path != exporterMetricsPath {
		t.Errorf("VMPodScrape endpoints = %+v", vs.Spec.PodMetricsEndpoints)
	}
}

func TestSyncScrapesRemovesLegacyAndConvertedResources(t *testing.T) {
	objectMeta := func(name string) metav1.ObjectMeta {
		return metav1.ObjectMeta{Name: name, Namespace: testNamespace}
	}
	legacyName := testInstance + legacyPodMonitorSuffix

	tests := []struct {
		name          string
		parameters    string
		wantConverted bool
	}{
		// The VictoriaMetrics Operator keeps the VMPodScrape it converts from
		// a live PodMonitor in sync itself.
		{name: "prometheus on keeps the converted VMPodScrape", parameters: prometheusOnly, wantConverted: true},
		{name: "prometheus off removes the converted VMPodScrape", parameters: victoriaMetricsOnly},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, cl := newTestContext(t, newMonitoredInstance(tt.parameters), interceptor.Funcs{},
				&monitoringv1.PodMonitor{ObjectMeta: objectMeta(legacyName)},
				&vmv1beta1.VMPodScrape{ObjectMeta: objectMeta(legacyName)},
				&vmv1beta1.VMPodScrape{ObjectMeta: objectMeta(podMonitorName(testInstance))},
			)
			if err := syncScrapes(c, ScrapeConfig{}); err != nil {
				t.Fatalf("syncScrapes: %v", err)
			}
			if scrapeExists(t, cl, &monitoringv1.PodMonitor{}, legacyName) {
				t.Error("legacy PodMonitor not removed")
			}
			if scrapeExists(t, cl, &vmv1beta1.VMPodScrape{}, legacyName) {
				t.Error("VMPodScrape converted from the legacy PodMonitor not removed")
			}
			if got := scrapeExists(t, cl, &vmv1beta1.VMPodScrape{}, podMonitorName(testInstance)); got != tt.wantConverted {
				t.Errorf("converted VMPodScrape exists = %v, want %v", got, tt.wantConverted)
			}
		})
	}
}

func TestSyncScrapesWithoutCRDs(t *testing.T) {
	funcs := interceptor.Funcs{
		Delete: func(_ context.Context, _ client.WithWatch, obj client.Object, _ ...client.DeleteOption) error {
			return &meta.NoKindMatchError{GroupKind: obj.GetObjectKind().GroupVersionKind().GroupKind()}
		},
	}
	for _, in := range []*corev1alpha1.Instance{newTestInstance("", false), newMonitoredInstance("")} {
		c, _ := newTestContext(t, in, funcs)
		if err := syncScrapes(c, ScrapeConfig{}); err != nil {
			t.Errorf("syncScrapes without the CRDs: %v", err)
		}
	}
}

func TestValidateScrapes(t *testing.T) {
	podMonitorGVK := monitoringv1.SchemeGroupVersion.WithKind(monitoringv1.PodMonitorsKind)
	vmPodScrapeGVK := vmv1beta1.GroupVersion.WithKind("VMPodScrape")

	tests := []struct {
		name       string
		parameters string
		served     []schema.GroupVersionKind
		wantErr    string
	}{
		{name: "nothing enabled needs no operator", parameters: ""},
		{name: "prometheus with its operator", parameters: prometheusOnly, served: []schema.GroupVersionKind{podMonitorGVK}},
		{name: "prometheus without its operator", parameters: prometheusOnly, served: []schema.GroupVersionKind{vmPodScrapeGVK}, wantErr: "Prometheus Operator"},
		{name: "victoriametrics with its operator", parameters: victoriaMetricsOnly, served: []schema.GroupVersionKind{vmPodScrapeGVK}},
		{name: "victoriametrics without its operator", parameters: victoriaMetricsOnly, served: []schema.GroupVersionKind{podMonitorGVK}, wantErr: "VictoriaMetrics Operator"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mapper := meta.NewDefaultRESTMapper(nil)
			for _, gvk := range tt.served {
				mapper.Add(gvk, meta.RESTScopeNamespace)
			}
			cl := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithRESTMapper(mapper).Build()
			c := controller.NewContext(context.Background(), cl, newMonitoredInstance(tt.parameters), common.ProviderName)

			err := validateScrapes(c)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateScrapes: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateScrapes() error = %v, want one naming the %s", err, tt.wantErr)
			}
		})
	}
}
