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
