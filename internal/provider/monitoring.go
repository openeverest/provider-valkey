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

	valkeyv1alpha1 "github.com/valkey-io/valkey-operator/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-valkey/internal/common"
)

const (
	// exporterPortName is the container port the valkey-operator gives the
	// metrics exporter sidecar.
	exporterPortName    = "metrics"
	exporterMetricsPath = "/metrics"
)

var _ controller.MonitoringSourcesProvider = (*Provider)(nil)

// MonitoringSources implements controller.MonitoringSourcesProvider. Valkey
// serves no metrics itself (metrics: Exporter in definition/monitoring.yaml):
// the redis_exporter sidecar in every engine pod does. Its endpoint is
// published while the live ValkeyCluster runs the sidecar and a ValkeyNode
// running it is ready, so rolling the pods keeps it published. Monitoring
// extensions turn it into scrape objects.
func (p *Provider) MonitoringSources(c *controller.Context) (*corev1alpha1.MonitoringSources, error) {
	live, err := liveCluster(c)
	if err != nil {
		return nil, err
	}
	sources := &corev1alpha1.MonitoringSources{}
	if live == nil || !exporterEnabled(live.Spec.Exporter) {
		return sources, nil
	}
	serving, err := exporterServing(c)
	if err != nil {
		return nil, err
	}
	if !serving {
		return sources, nil
	}
	sources.Metrics = []corev1alpha1.MetricsEndpoint{{
		Component: common.ComponentEngine,
		Kind:      common.KindValkey,
		PodSelector: map[string]string{
			controller.InstanceLabel:  c.Name(),
			controller.ComponentLabel: common.ComponentEngine,
		},
		Port:   intstr.FromString(exporterPortName),
		Path:   exporterMetricsPath,
		Scheme: "http",
	}}
	return sources, nil
}

// exporterRequired says whether the engine pods run the exporter sidecar
// this pass: always with the monitoring component, which users add to scrape
// it themselves; otherwise while a bound class requires metrics or, while
// that is unresolved, when the live ValkeyCluster already runs it (removing
// and re-adding the sidecar would roll the pods twice for nothing).
func exporterRequired(c *controller.Context) (bool, error) {
	if _, ok := c.Instance().Spec.Components[common.ComponentMonitoring]; ok {
		return true, nil
	}
	req, err := c.RequiredSources()
	if err != nil {
		return false, fmt.Errorf("reading what the monitoring destinations require: %w", err)
	}
	live, err := liveCluster(c)
	if err != nil {
		return false, err
	}
	return req.Metrics.Else(live != nil && exporterEnabled(live.Spec.Exporter)), nil
}

// exporterEnabled reports whether a ValkeyCluster runs the exporter sidecar:
// the operator runs it unless it is turned off.
func exporterEnabled(exporter valkeyv1alpha1.ExporterSpec) bool {
	return exporter.Enabled == nil || *exporter.Enabled
}

// exporterServing reports whether a ValkeyNode of the instance that runs the
// exporter sidecar is ready, having observed its spec. A ValkeyNode runs the
// sidecar only when it is explicitly enabled.
func exporterServing(c *controller.Context) (bool, error) {
	nodes := &valkeyv1alpha1.ValkeyNodeList{}
	if err := c.List(nodes, client.MatchingLabels{clusterLabel: c.Name()}); err != nil {
		return false, fmt.Errorf("listing ValkeyNodes: %w", err)
	}
	for _, node := range nodes.Items {
		enabled := node.Spec.Exporter.Enabled != nil && *node.Spec.Exporter.Enabled
		if enabled && node.Status.Ready && node.Status.ObservedGeneration == node.Generation {
			return true, nil
		}
	}
	return false, nil
}

// liveCluster returns the ValkeyCluster as the cluster holds it, nil before
// it exists.
func liveCluster(c *controller.Context) (*valkeyv1alpha1.ValkeyCluster, error) {
	vc := &valkeyv1alpha1.ValkeyCluster{}
	if err := c.Get(vc, c.Name()); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("getting the ValkeyCluster: %w", err)
	}
	return vc, nil
}

// The scrape resources of earlier releases, deleted only as metadata so the
// provider needs neither their types nor their CRDs.
var (
	podMonitorGVK  = schema.GroupVersionKind{Group: "monitoring.coreos.com", Version: "v1", Kind: "PodMonitor"}
	vmPodScrapeGVK = schema.GroupVersionKind{Group: "operator.victoriametrics.com", Version: "v1beta1", Kind: "VMPodScrape"}
)

// legacyScrapes lists the scrape resources releases before the monitoring
// contract created for an instance: the PodMonitors <name>-metrics (the
// oldest) and <name>-prometheus, the VMPodScrape <name>-victoriametrics, and
// the VMPodScrapes the VictoriaMetrics Operator converts from PodMonitors
// under the same names, which carry no owner reference.
func legacyScrapes(c *controller.Context) []*metav1.PartialObjectMetadata {
	var objs []*metav1.PartialObjectMetadata
	add := func(gvk schema.GroupVersionKind, suffix string) {
		obj := &metav1.PartialObjectMetadata{ObjectMeta: c.ObjectMeta(c.Name() + suffix)}
		obj.SetGroupVersionKind(gvk)
		objs = append(objs, obj)
	}
	for _, suffix := range []string{"-metrics", "-prometheus"} {
		add(podMonitorGVK, suffix)
		add(vmPodScrapeGVK, suffix)
	}
	add(vmPodScrapeGVK, "-victoriametrics")
	return objs
}

// deleteLegacyScrapes removes the scrape resources earlier releases created;
// monitoring extensions now create them from the published sources. A kind
// the cluster does not serve has nothing to remove.
// TODO: drop once no supported release created scrape resources.
func deleteLegacyScrapes(c *controller.Context) error {
	for _, obj := range legacyScrapes(c) {
		if err := c.Delete(obj); err != nil && !meta.IsNoMatchError(err) {
			return fmt.Errorf("deleting the legacy %s %s: %w", obj.Kind, obj.Name, err)
		}
	}
	return nil
}
