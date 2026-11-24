// Package components contains custom spec types for provider component types.
//
// Each struct here corresponds to a component type defined in versions.yaml
// and is converted to an OpenAPI schema during generation.
// Add fields when a component type needs custom configuration beyond
// what the base Instance spec provides.
//
// +k8s:openapi-gen=true
package components

// ValkeyEngineConfig holds custom configuration for the Valkey engine component.
type ValkeyEngineConfig struct {
	// Config holds additional Valkey configuration parameters that are passed
	// through verbatim to the underlying ValkeyCluster (for example
	// "maxmemory" or "maxmemory-policy"). Operator-managed keys such as port,
	// TLS, and ACL settings are ignored.
	// +optional
	Config map[string]string `json:"config,omitempty"`
}

// MonitoringParameters holds the configuration of the monitoring component,
// which runs the metrics exporter sidecar next to every Valkey server.
//
// Deprecated: the parameters have no effect. Earlier releases created a
// PodMonitor or VMPodScrape from them; to collect metrics, list a monitoring
// destination in spec.monitoring.destinations, or scrape the exporter
// yourself. They are still accepted, since Instances created by earlier UIs
// carry them.
type MonitoringParameters struct {
	// Deprecated: has no effect. Earlier releases created the scrape
	// resources it lists; list a monitoring destination in
	// spec.monitoring.destinations instead.
	// +optional
	Metrics *MetricsParameters `json:"metrics,omitempty"`
}

// MetricsParameters is deprecated and has no effect.
type MetricsParameters struct {
	// Deprecated: has no effect. Earlier releases created a PodMonitor for
	// the Prometheus Operator.
	// +optional
	Prometheus *ScrapeParameters `json:"prometheus,omitempty"`

	// Deprecated: has no effect. Earlier releases created a VMPodScrape for
	// the VictoriaMetrics Operator.
	// +optional
	VictoriaMetrics *ScrapeParameters `json:"victoriaMetrics,omitempty"`
}

// ScrapeParameters is deprecated and has no effect.
type ScrapeParameters struct {
	// Deprecated: has no effect.
	// +optional
	Enabled bool `json:"enabled,omitempty"`
}
