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

	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/openeverest/provider-valkey/internal/common"
)

func TestPodMonitorConfigFromEnv(t *testing.T) {
	tests := []struct {
		name    string
		enabled string
		labels  string
		want    PodMonitorConfig
		wantErr bool
	}{
		{name: "enabled by default", want: PodMonitorConfig{Enabled: true}},
		{name: "disabled", enabled: "false", want: PodMonitorConfig{}},
		{
			name:   "labels from JSON",
			labels: `{"release":"kube-prometheus-stack"}`,
			want:   PodMonitorConfig{Enabled: true, Labels: map[string]string{"release": "kube-prometheus-stack"}},
		},
		{name: "invalid enabled", enabled: "maybe", wantErr: true},
		{name: "invalid labels", labels: "release=kps", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(podMonitorEnabledEnv, tt.enabled)
			t.Setenv(podMonitorLabelsEnv, tt.labels)

			got, err := PodMonitorConfigFromEnv()
			if (err != nil) != tt.wantErr {
				t.Fatalf("PodMonitorConfigFromEnv() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if got.Enabled != tt.want.Enabled || len(got.Labels) != len(tt.want.Labels) ||
				got.Labels["release"] != tt.want.Labels["release"] {
				t.Fatalf("PodMonitorConfigFromEnv() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestSyncPodMonitor(t *testing.T) {
	cfg := PodMonitorConfig{
		Enabled: true,
		Labels: map[string]string{
			"release":                    "kube-prometheus-stack",
			"app.kubernetes.io/instance": "spoofed",
		},
	}
	in := newTestInstance("", true)
	c, cl := newTestContext(t, in, interceptor.Funcs{})

	if err := syncPodMonitor(c, cfg); err != nil {
		t.Fatalf("syncPodMonitor: %v", err)
	}
	pm := &monitoringv1.PodMonitor{}
	if err := cl.Get(context.Background(), client.ObjectKey{Namespace: testNamespace, Name: podMonitorName(testInstance)}, pm); err != nil {
		t.Fatalf("getting PodMonitor: %v", err)
	}

	// plugin-metrics finds the PodMonitor by the managed labels, which the
	// configured ones must not override.
	if got := pm.Labels["app.kubernetes.io/instance"]; got != testInstance {
		t.Errorf("instance label = %q, want %q", got, testInstance)
	}
	if got := pm.Labels["app.kubernetes.io/managed-by"]; got != "everest" {
		t.Errorf("managed-by label = %q, want everest", got)
	}
	if got := pm.Labels["release"]; got != "kube-prometheus-stack" {
		t.Errorf("release label = %q, want kube-prometheus-stack", got)
	}
	if got := pm.Spec.Selector.MatchLabels[clusterPodLabel]; got != testInstance {
		t.Errorf("selector %s = %q, want %q", clusterPodLabel, got, testInstance)
	}
	if len(pm.Spec.PodMetricsEndpoints) != 1 || *pm.Spec.PodMetricsEndpoints[0].Port != exporterPortName {
		t.Errorf("endpoints = %+v, want the %q port", pm.Spec.PodMetricsEndpoints, exporterPortName)
	}

	// Dropping the monitoring component removes the PodMonitor.
	delete(in.Spec.Components, common.ComponentMonitoring)
	if err := syncPodMonitor(c, cfg); err != nil {
		t.Fatalf("syncPodMonitor without monitoring: %v", err)
	}
	err := cl.Get(context.Background(), client.ObjectKey{Namespace: testNamespace, Name: podMonitorName(testInstance)}, pm)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("PodMonitor still present after removing monitoring: %v", err)
	}
}

func TestSyncPodMonitorWithoutCRD(t *testing.T) {
	noMatch := &meta.NoKindMatchError{GroupKind: schema.GroupKind{Group: "monitoring.coreos.com", Kind: "PodMonitor"}}
	funcs := interceptor.Funcs{
		Apply: func(context.Context, client.WithWatch, runtime.ApplyConfiguration, ...client.ApplyOption) error {
			return noMatch
		},
		Delete: func(context.Context, client.WithWatch, client.Object, ...client.DeleteOption) error {
			return noMatch
		},
	}

	for _, monitoring := range []bool{true, false} {
		c, _ := newTestContext(t, newTestInstance("", monitoring), funcs)
		if err := syncPodMonitor(c, PodMonitorConfig{Enabled: true}); err != nil {
			t.Errorf("syncPodMonitor(monitoring=%v) without the CRD: %v", monitoring, err)
		}
	}
}
