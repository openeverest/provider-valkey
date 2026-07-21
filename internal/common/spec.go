// Package common defines shared constants used across the provider.
package common

const (
	// ProviderName is the canonical name of this provider. It must match the
	// `name` field in definition/provider.yaml and the value users set in
	// Instance.spec.provider.
	ProviderName = "valkey"

	// Component names — the logical roles referenced by topologies and the
	// reconciliation code.
	ComponentEngine     = "engine"
	ComponentMonitoring = "monitoring"

	// Component types — the software each component runs.
	ComponentTypeValkey   = "valkey"
	ComponentTypeExporter = "exporter"

	// Topology names — must match the directory names under definition/topologies.
	TopologyReplication = "replication"
	TopologyCluster     = "cluster"
)
