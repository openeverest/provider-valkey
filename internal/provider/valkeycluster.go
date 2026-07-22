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

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	valkeyv1alpha1 "github.com/valkey-io/valkey-operator/api/v1alpha1"

	"github.com/openeverest/provider-valkey/definition/components"
	"github.com/openeverest/provider-valkey/definition/topologies/cluster"
	"github.com/openeverest/provider-valkey/internal/common"
)

const (
	// minClusterShards is the minimum number of shards for the cluster topology.
	// Valkey cluster mode requires at least three primaries.
	minClusterShards = 3

	// defaultClusterShards is used for the cluster topology when the user does
	// not specify a shard count.
	defaultClusterShards = 3

	// valkeyPort is the default Valkey client port exposed by the operator.
	valkeyPort = "6379"

	// headlessServicePrefix mirrors the valkey-operator's headless Service
	// naming (`valkey-<clusterName>`).
	headlessServicePrefix = "valkey-"
)

// buildClusterSpec translates an Instance spec into a ValkeyClusterSpec.
func buildClusterSpec(c *controller.Context) (valkeyv1alpha1.ValkeyClusterSpec, error) {
	var spec valkeyv1alpha1.ValkeyClusterSpec

	engine := c.Instance().Spec.Components[common.ComponentEngine]

	shards, err := resolveShards(c)
	if err != nil {
		return spec, err
	}
	spec.Shards = shards

	if engine.Replicas != nil {
		spec.Replicas = *engine.Replicas
	}

	image, err := resolveEngineImage(c, engine)
	if err != nil {
		return spec, err
	}
	spec.Image = image

	if engine.Resources != nil {
		spec.Resources = *engine.Resources
	}

	if engine.Storage != nil && !engine.Storage.Size.IsZero() {
		spec.Persistence = &valkeyv1alpha1.PersistenceSpec{
			Size:             engine.Storage.Size,
			StorageClassName: engine.Storage.StorageClass,
		}
	}

	// Pass through any custom Valkey configuration parameters.
	var engineCfg components.ValkeyEngineConfig
	if c.TryDecodeComponentParameters(engine, &engineCfg) && len(engineCfg.Config) > 0 {
		spec.Config = engineCfg.Config
	}

	// The exporter sidecar is enabled only when the optional monitoring
	// component is present on the Instance.
	exporter, err := buildExporterSpec(c)
	if err != nil {
		return spec, err
	}
	spec.Exporter = exporter

	return spec, nil
}

// buildExporterSpec configures the metrics exporter based on the presence of
// the monitoring component.
func buildExporterSpec(c *controller.Context) (valkeyv1alpha1.ExporterSpec, error) {
	monitoring, ok := c.Instance().Spec.Components[common.ComponentMonitoring]
	if !ok {
		return valkeyv1alpha1.ExporterSpec{Enabled: false}, nil
	}

	exporter := valkeyv1alpha1.ExporterSpec{Enabled: true}

	if monitoring.Image != "" {
		exporter.Image = monitoring.Image
	} else {
		spec, err := c.ProviderSpec()
		if err != nil {
			return exporter, err
		}
		if monitoring.Version != "" {
			exporter.Image = controller.GetImageForVersion(spec, common.ComponentMonitoring, monitoring.Version)
		}
		if exporter.Image == "" {
			exporter.Image = controller.GetDefaultImageForComponent(spec, common.ComponentMonitoring)
		}
	}

	if monitoring.Resources != nil {
		exporter.Resources = *monitoring.Resources
	}

	return exporter, nil
}

// resolveEngineImage resolves the Valkey server image from the user override,
// the selected version bundle, or the provider default.
func resolveEngineImage(c *controller.Context, engine corev1alpha1.ComponentSpec) (string, error) {
	if engine.Image != "" {
		return engine.Image, nil
	}

	spec, err := c.ProviderSpec()
	if err != nil {
		return "", err
	}

	image := ""
	if engine.Version != "" {
		image = controller.GetImageForVersion(spec, common.ComponentEngine, engine.Version)
	}
	if image == "" {
		image = controller.GetDefaultImageForComponent(spec, common.ComponentEngine)
	}
	return image, nil
}

// resolveShards returns the number of shards for the Instance's topology.
// The replication topology is always a single shard; the cluster topology
// reads numShards from its topology config (defaulting to defaultClusterShards).
func resolveShards(c *controller.Context) (int32, error) {
	if c.Instance().GetTopologyType() != common.TopologyCluster {
		return 1, nil
	}

	var cfg cluster.ClusterTopologyConfig
	if c.TryDecodeTopologyParameters(&cfg) && cfg.NumShards > 0 {
		return cfg.NumShards, nil
	}
	return defaultClusterShards, nil
}

// buildConnectionDetails returns the well-known connection details for the
// cluster's headless Service. Authentication is not configured in this version
// of the provider, so no credentials are included.
func buildConnectionDetails(c *controller.Context) controller.ConnectionDetails {
	host := fmt.Sprintf("%s%s.%s.svc.cluster.local", headlessServicePrefix, c.Name(), c.Namespace())
	return controller.ConnectionDetails{
		Type:     "valkey",
		Provider: common.ProviderName,
		Host:     host,
		Port:     valkeyPort,
		URI:      fmt.Sprintf("redis://%s:%s", host, valkeyPort),
	}
}
