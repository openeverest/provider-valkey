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

	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-valkey/definition/components"
	vmv1beta1 "github.com/openeverest/provider-valkey/internal/apis/victoriametrics/v1beta1"
	"github.com/openeverest/provider-valkey/internal/common"
)

const (
	// The scrape resources are named apart: the VictoriaMetrics Operator
	// converts a PodMonitor into a VMPodScrape of the same name by default.
	podMonitorSuffix  = "-prometheus"
	vmPodScrapeSuffix = "-victoriametrics"
	// legacyPodMonitorSuffix names the PodMonitor earlier provider versions
	// created for every instance with the monitoring component.
	legacyPodMonitorSuffix = "-metrics"

	// exporterPortName is the container port the valkey-operator gives the
	// metrics exporter sidecar.
	exporterPortName    = "metrics"
	exporterMetricsPath = "/metrics"

	shardIndexPodLabel = "valkey.io/shard-index"
	nodeIndexPodLabel  = "valkey.io/node-index"

	podMonitorLabelsEnv  = "POD_MONITOR_LABELS"
	vmPodScrapeLabelsEnv = "VM_POD_SCRAPE_LABELS"
)

var (
	podMonitorGroupKind  = schema.GroupKind{Group: monitoringv1.SchemeGroupVersion.Group, Kind: monitoringv1.PodMonitorsKind}
	vmPodScrapeGroupKind = schema.GroupKind{Group: vmv1beta1.GroupVersion.Group, Kind: "VMPodScrape"}
)

// ScrapeConfig holds the labels the Helm chart adds to every scrape resource,
// so that the monitoring system's selector picks them up (e.g. release:
// kube-prometheus-stack for its podMonitorSelector).
type ScrapeConfig struct {
	PodMonitorLabels  map[string]string
	VMPodScrapeLabels map[string]string
}

// ScrapeConfigFromEnv reads the scrape configuration set by the Helm chart:
// POD_MONITOR_LABELS and VM_POD_SCRAPE_LABELS hold the labels as JSON objects.
func ScrapeConfigFromEnv() (ScrapeConfig, error) {
	var cfg ScrapeConfig
	for env, labels := range map[string]*map[string]string{
		podMonitorLabelsEnv:  &cfg.PodMonitorLabels,
		vmPodScrapeLabelsEnv: &cfg.VMPodScrapeLabels,
	} {
		if value := os.Getenv(env); value != "" {
			if err := json.Unmarshal([]byte(value), labels); err != nil {
				return cfg, fmt.Errorf("parsing %s: %w", env, err)
			}
		}
	}
	return cfg, nil
}

func podMonitorName(instance string) string {
	return instance + podMonitorSuffix
}

func vmPodScrapeName(instance string) string {
	return instance + vmPodScrapeSuffix
}

// scrapes lists the monitoring systems that scrape the instance.
type scrapes struct {
	prometheus      bool
	victoriaMetrics bool
}

// enabledScrapes reads the monitoring systems the monitoring component's
// parameters enable. Without the component nothing is scraped.
func enabledScrapes(c *controller.Context) (scrapes, error) {
	monitoring, ok := c.Instance().Spec.Components[common.ComponentMonitoring]
	if !ok || monitoring.Parameters == nil || len(monitoring.Parameters.Raw) == 0 {
		return scrapes{}, nil
	}
	params := &components.MonitoringParameters{}
	if err := c.DecodeComponentParameters(monitoring, params); err != nil {
		return scrapes{}, fmt.Errorf("decoding the monitoring parameters: %w", err)
	}
	if params.Metrics == nil {
		return scrapes{}, nil
	}
	return scrapes{
		prometheus:      scrapeEnabled(params.Metrics.Prometheus),
		victoriaMetrics: scrapeEnabled(params.Metrics.VictoriaMetrics),
	}, nil
}

func scrapeEnabled(scrape *components.ScrapeParameters) bool {
	return scrape != nil && scrape.Enabled
}

// validateScrapes rejects scraping by a monitoring system whose operator is
// not installed, since its scrape resource could not be created.
func validateScrapes(c *controller.Context) error {
	enabled, err := enabledScrapes(c)
	if err != nil {
		return err
	}
	checks := []struct {
		enabled  bool
		kind     schema.GroupKind
		version  string
		operator string
	}{
		{enabled.prometheus, podMonitorGroupKind, monitoringv1.Version, "Prometheus Operator"},
		{enabled.victoriaMetrics, vmPodScrapeGroupKind, vmv1beta1.GroupVersion.Version, "VictoriaMetrics Operator"},
	}
	for _, check := range checks {
		if !check.enabled {
			continue
		}
		if _, err := c.Client().RESTMapper().RESTMapping(check.kind, check.version); err != nil {
			if meta.IsNoMatchError(err) {
				return fmt.Errorf("the %s is not installed: the cluster does not serve %s", check.operator, check.kind)
			}
			return fmt.Errorf("looking up %s: %w", check.kind, err)
		}
	}
	return nil
}

// syncScrapes creates the scrape resource of every monitoring system the
// monitoring component enables, and removes the others.
func syncScrapes(c *controller.Context, cfg ScrapeConfig) error {
	enabled, err := enabledScrapes(c)
	if err != nil {
		return err
	}

	// The PodMonitor of earlier provider versions, and the VMPodScrape the
	// VictoriaMetrics Operator converted from it.
	legacy := c.ObjectMeta(c.Name() + legacyPodMonitorSuffix)
	if err := deleteIfServed(c, &monitoringv1.PodMonitor{ObjectMeta: legacy}); err != nil {
		return fmt.Errorf("deleting the legacy PodMonitor: %w", err)
	}
	if err := deleteIfServed(c, &vmv1beta1.VMPodScrape{ObjectMeta: legacy}); err != nil {
		return fmt.Errorf("deleting the VMPodScrape converted from the legacy PodMonitor: %w", err)
	}

	podMonitor := &monitoringv1.PodMonitor{ObjectMeta: c.ObjectMeta(podMonitorName(c.Name()))}
	if err := syncScrape(c, enabled.prometheus, buildPodMonitor(c, cfg.PodMonitorLabels), podMonitor); err != nil {
		return fmt.Errorf("syncing the PodMonitor: %w", err)
	}
	if !enabled.prometheus {
		// The converter sets no owner reference on the VMPodScrape, so it
		// does not always go with the PodMonitor.
		converted := &vmv1beta1.VMPodScrape{ObjectMeta: c.ObjectMeta(podMonitorName(c.Name()))}
		if err := deleteIfServed(c, converted); err != nil {
			return fmt.Errorf("deleting the VMPodScrape converted from the PodMonitor: %w", err)
		}
	}

	vmPodScrape := &vmv1beta1.VMPodScrape{ObjectMeta: c.ObjectMeta(vmPodScrapeName(c.Name()))}
	if err := syncScrape(c, enabled.victoriaMetrics, buildVMPodScrape(c, cfg.VMPodScrapeLabels), vmPodScrape); err != nil {
		return fmt.Errorf("syncing the VMPodScrape: %w", err)
	}
	return nil
}

func syncScrape(c *controller.Context, enabled bool, desired, existing client.Object) error {
	if enabled {
		return c.Apply(desired)
	}
	return deleteIfServed(c, existing)
}

// deleteIfServed deletes obj; there is nothing to remove when the cluster
// does not serve its kind at all.
func deleteIfServed(c *controller.Context, obj client.Object) error {
	if err := c.Delete(obj); err != nil && !meta.IsNoMatchError(err) {
		return err
	}
	return nil
}

// scrapeObjectMeta names a scrape resource of the instance. The configured
// labels never override the managed ones, by which plugin-metrics finds it.
func scrapeObjectMeta(c *controller.Context, name string, extraLabels map[string]string) metav1.ObjectMeta {
	objectMeta := c.ObjectMeta(name)
	labels := maps.Clone(extraLabels)
	if labels == nil {
		labels = map[string]string{}
	}
	maps.Copy(labels, objectMeta.Labels)
	objectMeta.Labels = labels
	return objectMeta
}

// scrapeSelector selects the pods by the operator's own label, which they
// carry before they roll to pick up the OpenEverest pod labels.
func scrapeSelector(c *controller.Context) metav1.LabelSelector {
	return metav1.LabelSelector{MatchLabels: map[string]string{clusterPodLabel: c.Name()}}
}

// scrapeTargetLabels are the pod labels copied onto every scraped series.
func scrapeTargetLabels() []string {
	return []string{
		controller.InstanceLabel, controller.ComponentLabel,
		shardIndexPodLabel, nodeIndexPodLabel,
	}
}

func buildPodMonitor(c *controller.Context, extraLabels map[string]string) *monitoringv1.PodMonitor {
	return &monitoringv1.PodMonitor{
		ObjectMeta: scrapeObjectMeta(c, podMonitorName(c.Name()), extraLabels),
		Spec: monitoringv1.PodMonitorSpec{
			Selector:        scrapeSelector(c),
			PodTargetLabels: scrapeTargetLabels(),
			PodMetricsEndpoints: []monitoringv1.PodMetricsEndpoint{{
				Port: ptr.To(exporterPortName),
				Path: exporterMetricsPath,
			}},
		},
	}
}

func buildVMPodScrape(c *controller.Context, extraLabels map[string]string) *vmv1beta1.VMPodScrape {
	return &vmv1beta1.VMPodScrape{
		ObjectMeta: scrapeObjectMeta(c, vmPodScrapeName(c.Name()), extraLabels),
		Spec: vmv1beta1.VMPodScrapeSpec{
			Selector:        scrapeSelector(c),
			PodTargetLabels: scrapeTargetLabels(),
			PodMetricsEndpoints: []vmv1beta1.PodMetricsEndpoint{{
				Port: ptr.To(exporterPortName),
				Path: exporterMetricsPath,
			}},
		},
	}
}
