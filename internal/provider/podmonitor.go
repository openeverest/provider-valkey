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
	"fmt"
	"maps"
	"os"
	"strconv"

	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-valkey/internal/common"
)

const (
	podMonitorSuffix = "-metrics"

	// exporterPortName is the container port the valkey-operator gives the
	// metrics exporter sidecar.
	exporterPortName    = "metrics"
	exporterMetricsPath = "/metrics"

	shardIndexPodLabel = "valkey.io/shard-index"
	nodeIndexPodLabel  = "valkey.io/node-index"

	// componentMetricLabel is controller.ComponentLabel as a Prometheus label.
	// The operator cannot label the pods with it, so the scrape sets it.
	componentMetricLabel = "core_openeverest_io_component"

	podMonitorEnabledEnv = "POD_MONITOR_ENABLED"
	podMonitorLabelsEnv  = "POD_MONITOR_LABELS"
)

// PodMonitorConfig configures the Prometheus Operator PodMonitor created for
// instances that have the monitoring component.
type PodMonitorConfig struct {
	Enabled bool
	// Labels are added to every PodMonitor so that the Prometheus
	// podMonitorSelector picks them up (e.g. release: kube-prometheus-stack).
	Labels map[string]string
}

// PodMonitorConfigFromEnv reads the PodMonitor configuration set by the Helm
// chart. PodMonitors are enabled unless POD_MONITOR_ENABLED says otherwise;
// POD_MONITOR_LABELS holds the extra labels as a JSON object.
func PodMonitorConfigFromEnv() (PodMonitorConfig, error) {
	cfg := PodMonitorConfig{Enabled: true}
	if value, ok := os.LookupEnv(podMonitorEnabledEnv); ok && value != "" {
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			return cfg, fmt.Errorf("parsing %s: %w", podMonitorEnabledEnv, err)
		}
		cfg.Enabled = enabled
	}
	if value := os.Getenv(podMonitorLabelsEnv); value != "" {
		if err := json.Unmarshal([]byte(value), &cfg.Labels); err != nil {
			return cfg, fmt.Errorf("parsing %s: %w", podMonitorLabelsEnv, err)
		}
	}
	return cfg, nil
}

func podMonitorName(instance string) string {
	return instance + podMonitorSuffix
}

// syncPodMonitor creates the PodMonitor that scrapes the exporter sidecars, or
// removes it once the monitoring component is gone. Without the Prometheus
// Operator CRDs the exporter still runs and the PodMonitor is skipped.
func syncPodMonitor(c *controller.Context, cfg PodMonitorConfig) error {
	_, monitored := c.Instance().Spec.Components[common.ComponentMonitoring]
	if !cfg.Enabled || !monitored {
		err := c.Delete(&monitoringv1.PodMonitor{ObjectMeta: c.ObjectMeta(podMonitorName(c.Name()))})
		if err != nil && !meta.IsNoMatchError(err) {
			return fmt.Errorf("deleting PodMonitor: %w", err)
		}
		return nil
	}

	err := c.Apply(buildPodMonitor(c, cfg.Labels))
	if meta.IsNoMatchError(err) {
		log.FromContext(c.Context()).V(1).Info("PodMonitor CRD not installed, skipping the PodMonitor", "instance", c.Name())
		return nil
	}
	if err != nil {
		return fmt.Errorf("applying PodMonitor: %w", err)
	}
	return nil
}

// buildPodMonitor builds the PodMonitor for the instance. The configured
// labels never override the managed ones that identify the owning Instance.
func buildPodMonitor(c *controller.Context, extraLabels map[string]string) *monitoringv1.PodMonitor {
	objectMeta := c.ObjectMeta(podMonitorName(c.Name()))
	labels := maps.Clone(extraLabels)
	if labels == nil {
		labels = map[string]string{}
	}
	maps.Copy(labels, objectMeta.Labels)
	objectMeta.Labels = labels

	return &monitoringv1.PodMonitor{
		ObjectMeta: objectMeta,
		Spec: monitoringv1.PodMonitorSpec{
			Selector:        metav1.LabelSelector{MatchLabels: map[string]string{clusterPodLabel: c.Name()}},
			PodTargetLabels: []string{shardIndexPodLabel, nodeIndexPodLabel},
			PodMetricsEndpoints: []monitoringv1.PodMetricsEndpoint{{
				Port: ptr.To(exporterPortName),
				Path: exporterMetricsPath,
				RelabelConfigs: []monitoringv1.RelabelConfig{{
					Action:      "replace",
					TargetLabel: componentMetricLabel,
					Replacement: ptr.To(common.ComponentEngine),
				}},
			}},
		},
	}
}
