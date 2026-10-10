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

	// TLS configures transport encryption for the Valkey instance. Transport
	// encryption is enabled by default; the provider generates a self-signed
	// certificate authority and server certificate when no external secret is
	// present.
	// +optional
	TLS *ValkeyTLSConfig `json:"tls,omitempty"`
}

// ValkeyTLSConfig configures in-transit encryption for the Valkey instance.
type ValkeyTLSConfig struct {
	// Mode controls transport encryption. "enabled" (the default when unset)
	// provisions a self-signed CA and server certificate; "disabled" turns
	// transport encryption off.
	// +kubebuilder:validation:Enum=enabled;disabled
	// +optional
	Mode string `json:"mode,omitempty"`

	// ClientAuth controls client certificate authentication. "optional" (the
	// default when unset) accepts clients with or without a certificate,
	// "required" enforces mutual TLS, and "disabled" ignores client
	// certificates. The provider issues a client certificate signed by the
	// instance CA and publishes it in the connection details.
	// +kubebuilder:validation:Enum=optional;required;disabled
	// +optional
	ClientAuth string `json:"clientAuth,omitempty"`

	// CertificateUser maps a client certificate to the ACL user named by its
	// Common Name, so clients authenticate without a password. "cn" enables
	// the mapping (the issued client certificate has CN=default and requires
	// Valkey 9.0+); "disabled" (the default when unset) turns it off.
	// +kubebuilder:validation:Enum=cn;disabled
	// +optional
	CertificateUser string `json:"certificateUser,omitempty"`
}

const (
	TLSModeEnabled  = "enabled"
	TLSModeDisabled = "disabled"

	TLSClientAuthOptional = "optional"
	TLSClientAuthRequired = "required"
	TLSClientAuthDisabled = "disabled"

	TLSCertificateUserCN       = "cn"
	TLSCertificateUserDisabled = "disabled"
)
