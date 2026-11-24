// Package definition contains shared types used across the provider definition.
//
// +k8s:openapi-gen=true
package definition

// TopologyType identifies a Valkey deployment topology.
type TopologyType string

const (
	// TopologyTypeReplication is a single-shard deployment (one primary with
	// zero or more replicas).
	TopologyTypeReplication TopologyType = "replication"
	// TopologyTypeCluster is a multi-shard, sharded deployment.
	TopologyTypeCluster TopologyType = "cluster"
)

// Parameters are the instance-wide parameters (Instance.spec.parameters).
type Parameters struct {
	// TLS configures encryption in transit: between clients and Valkey, and
	// between the Valkey nodes (replication and the cluster bus).
	// +optional
	TLS *TLSParameters `json:"tls,omitempty"`
}

// TLSMode controls whether TLS is on.
// +enum
type TLSMode string

const (
	// TLSModeRequire serves clients and the other nodes over TLS only, on
	// port 6379. The default. valkey-operator turns the plaintext port off
	// with TLS, so preferTLS is not offered.
	TLSModeRequire TLSMode = "requireTLS"
	// TLSModeDisabled keeps TLS off. Can only be set when the instance is
	// created.
	TLSModeDisabled TLSMode = "disabled"
)

// TLSParameters configures TLS for the instance. The server certificate
// comes from, in order of precedence: certificateSecretName, the
// cert-manager issuer, or else a CA the provider creates for the instance.
// The mode and the certificate source cannot change once the instance is
// created.
type TLSParameters struct {
	// Mode is requireTLS or disabled.
	// +kubebuilder:validation:Enum=requireTLS;disabled
	// +default="requireTLS"
	// +optional
	Mode TLSMode `json:"mode,omitempty"`

	// CertificateSecretName names a Secret in the instance namespace with the
	// server certificate, followed by any intermediate CA certificates
	// (tls.crt), its private key (tls.key) and the CA certificate (ca.crt).
	// The Valkey nodes present it to clients and to each other, so it must be
	// valid for server and client authentication, and for the instance's
	// in-cluster names (see the provider README) as well as for the names
	// clients connect to. Does not need cert-manager. Cannot be combined with
	// issuer or additionalNames.
	// +optional
	CertificateSecretName string `json:"certificateSecretName,omitempty"`

	// Issuer is the cert-manager issuer that signs the server certificate
	// and the default user's client certificate, instead of the CA the
	// provider creates for the instance. The provider requests them as the
	// Certificates <instance>-tls-server and <instance>-tls-client.
	// +optional
	Issuer *IssuerReference `json:"issuer,omitempty"`

	// AdditionalNames are DNS names the server certificate is valid for
	// besides the instance's in-cluster names, such as an external name
	// clients connect to. Used with issuer or the provider's CA; cannot be
	// combined with certificateSecretName, whose certificate carries its own
	// names. On a running instance a change reissues the certificate and
	// restarts the Valkey pods, which waits for maintenance approval.
	// +optional
	AdditionalNames []string `json:"additionalNames,omitempty"`

	// ClientAuth controls client certificate authentication. "optional" (the
	// default) accepts clients with or without a certificate, "required"
	// enforces mutual TLS, and "disabled" ignores client certificates. With
	// the provider's CA or an issuer, the provider issues a client
	// certificate for the default user and publishes it in the connection
	// details.
	// +kubebuilder:validation:Enum=optional;required;disabled
	// +optional
	ClientAuth string `json:"clientAuth,omitempty"`

	// CertificateUser maps a client certificate to the ACL user named by its
	// Common Name, so clients authenticate without a password. "cn" enables
	// the mapping (the issued client certificate has CN=default and requires
	// Valkey 9.0+); "disabled" (the default) turns it off.
	// +kubebuilder:validation:Enum=cn;disabled
	// +optional
	CertificateUser string `json:"certificateUser,omitempty"`
}

const (
	TLSClientAuthOptional = "optional"
	TLSClientAuthRequired = "required"
	TLSClientAuthDisabled = "disabled"

	TLSCertificateUserCN       = "cn"
	TLSCertificateUserDisabled = "disabled"
)

// IssuerKind is the kind of a cert-manager issuer.
// +enum
type IssuerKind string

const (
	// IssuerKindIssuer is an Issuer in the instance namespace.
	IssuerKindIssuer IssuerKind = "Issuer"
	// IssuerKindClusterIssuer is a cluster-wide ClusterIssuer.
	IssuerKindClusterIssuer IssuerKind = "ClusterIssuer"
)

// IssuerReference refers to a cert-manager issuer.
type IssuerReference struct {
	// Name of the issuer.
	Name string `json:"name"`

	// Kind of the issuer: Issuer or ClusterIssuer, or the kind of an
	// external issuer of group.
	// +default="Issuer"
	// +optional
	Kind IssuerKind `json:"kind,omitempty"`

	// Group is the API group of an external issuer, such as
	// awspca.cert-manager.io.
	// +default="cert-manager.io"
	// +optional
	Group string `json:"group,omitempty"`
}
