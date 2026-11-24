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
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	valkeyv1alpha1 "github.com/valkey-io/valkey-operator/api/v1alpha1"

	"github.com/openeverest/provider-valkey/definition"
)

const (
	// tlsSecretSuffix is appended to the Instance name to form the TLS secret
	// mounted into the Valkey pods (CA certificate + server certificate).
	tlsSecretSuffix = "-tls"

	// tlsCASecretSuffix names the secret holding the instance CA and its
	// private key. It is never mounted into the pods; the provider signs
	// client certificates with it.
	tlsCASecretSuffix = "-tls-ca"

	// tlsClientSecretSuffix names the secret holding the client certificate
	// the provider issues for the default user, and with an issuer, the
	// cert-manager Certificate that writes it.
	tlsClientSecretSuffix = "-tls-client"

	// tlsIssuedSecretSuffix names, with an issuer, the cert-manager
	// Certificate for the server and the Secret it writes.
	tlsIssuedSecretSuffix = "-tls-server"

	// tlsNamesAnnotation records, on the server certificate's Secret (the
	// provider's CA) or Certificate (an issuer), a hash of the additional
	// names it was issued for. The Valkey pods carry the hash of the names
	// their certificate has, so a change of names rolls them.
	tlsNamesAnnotation = "valkey.openeverest.io/tls-names"

	// tlsCertValidity is the lifetime of the self-signed certificates. The
	// provider does not rotate them yet, so it is deliberately long-lived.
	tlsCertValidity = 10 * 365 * 24 * time.Hour

	// tlsRSABits is the RSA key size for the generated CA and leaf keys.
	tlsRSABits = 2048

	tlsSecretKeyCA   = "ca.crt"
	tlsSecretKeyCert = "tls.crt"
	tlsSecretKeyKey  = "tls.key"

	// certificateUserMinMajorVersion is the first Valkey major version with
	// tls-auth-clients-user CN.
	certificateUserMinMajorVersion = 9

	clientAuthRelaxedDetail = "cannot be changed from required on a running instance: " +
		"valkey-operator would lose access to the nodes that still require client certificates"
)

var tlsPath = field.NewPath("spec", "parameters", "tls")

// tlsSecretName returns the name of the TLS secret for the given instance.
func tlsSecretName(instance string) string {
	return instance + tlsSecretSuffix
}

func tlsCASecretName(instance string) string {
	return instance + tlsCASecretSuffix
}

func tlsClientSecretName(instance string) string {
	return instance + tlsClientSecretSuffix
}

func tlsIssuedSecretName(instance string) string {
	return instance + tlsIssuedSecretSuffix
}

// instanceTLS decodes the TLS parameters of in, with the defaults applied:
// requireTLS, and an Issuer of cert-manager.io.
func instanceTLS(in *corev1alpha1.Instance) (definition.TLSParameters, error) {
	var params definition.Parameters
	if raw := in.Spec.Parameters; raw != nil && len(raw.Raw) > 0 {
		if err := json.Unmarshal(raw.Raw, &params); err != nil {
			return definition.TLSParameters{}, fmt.Errorf("decode spec.parameters: %w", err)
		}
	}
	var tls definition.TLSParameters
	if params.TLS != nil {
		tls = *params.TLS
	}
	if tls.Mode == "" {
		tls.Mode = definition.TLSModeRequire
	}
	if tls.Issuer != nil {
		issuer := *tls.Issuer
		if issuer.Kind == "" {
			issuer.Kind = definition.IssuerKindIssuer
		}
		if issuer.Group == "" {
			issuer.Group = certManagerGroup
		}
		tls.Issuer = &issuer
	}
	return tls, nil
}

// tlsParameters returns the Instance's TLS parameters. The runtime has
// checked them against their schema, and Validate reports what does not
// decode.
func tlsParameters(in *corev1alpha1.Instance) definition.TLSParameters {
	tls, err := instanceTLS(in)
	if err != nil {
		return definition.TLSParameters{Mode: definition.TLSModeRequire}
	}
	return tls
}

// tlsEnabled reports whether transport encryption is enabled for the instance.
// TLS is on by default and can be turned off, at creation, with
// spec.parameters.tls.mode=disabled.
func tlsEnabled(c *controller.Context) bool {
	return tlsParameters(c.Instance()).Mode != definition.TLSModeDisabled
}

// tlsSource is where the server certificate comes from.
type tlsSource int

const (
	// tlsSourceManaged is the CA the provider creates for the instance.
	tlsSourceManaged tlsSource = iota
	// tlsSourceIssuer is a cert-manager issuer the user names.
	tlsSourceIssuer
	// tlsSourceSecret is a Secret the user provides.
	tlsSourceSecret
)

func certificateSourceOf(tls definition.TLSParameters) tlsSource {
	switch {
	case tls.CertificateSecretName != "":
		return tlsSourceSecret
	case tls.Issuer != nil:
		return tlsSourceIssuer
	}
	return tlsSourceManaged
}

// serverSecretName is the Secret the Valkey pods mount the server
// certificate from: the user's, the one cert-manager writes, or the one of
// the provider's CA.
func serverSecretName(instance string, tls definition.TLSParameters) string {
	switch certificateSourceOf(tls) {
	case tlsSourceSecret:
		return tls.CertificateSecretName
	case tlsSourceIssuer:
		return tlsIssuedSecretName(instance)
	}
	return tlsSecretName(instance)
}

// clientCertificatesAccepted reports whether Valkey verifies the client
// certificates it is presented, which is what makes the issued one useful.
func clientCertificatesAccepted(cfg definition.TLSParameters) bool {
	return cfg.ClientAuth != definition.TLSClientAuthDisabled
}

// clientCertificateNeeded reports whether clients cannot do without the
// issued client certificate.
func clientCertificateNeeded(cfg definition.TLSParameters) bool {
	return cfg.ClientAuth == definition.TLSClientAuthRequired ||
		cfg.CertificateUser == definition.TLSCertificateUserCN
}

// buildClientAuth maps the TLS parameters onto the operator's clientAuth. It
// returns nil when neither is set, keeping the operator default.
func buildClientAuth(cfg definition.TLSParameters) *valkeyv1alpha1.TLSClientAuthSpec {
	if cfg.ClientAuth == "" && cfg.CertificateUser == "" {
		return nil
	}
	clientAuth := &valkeyv1alpha1.TLSClientAuthSpec{}
	switch cfg.ClientAuth {
	case definition.TLSClientAuthRequired:
		clientAuth.Mode = valkeyv1alpha1.TLSAuthClientsRequired
	case definition.TLSClientAuthDisabled:
		clientAuth.Mode = valkeyv1alpha1.TLSAuthClientsDisabled
	default:
		clientAuth.Mode = valkeyv1alpha1.TLSAuthClientsOptional
	}
	if cfg.CertificateUser == definition.TLSCertificateUserCN {
		clientAuth.CertificateUser = valkeyv1alpha1.TLSAuthClientsUserCN
	} else {
		clientAuth.CertificateUser = valkeyv1alpha1.TLSAuthClientsUserDisabled
	}
	return clientAuth
}

// namesHash identifies a list of additional names, in any order.
func namesHash(names []string) string {
	sorted := slices.Sorted(slices.Values(names))
	sum := sha256.Sum256([]byte(strings.Join(slices.Compact(sorted), ",")))
	return hex.EncodeToString(sum[:])[:16]
}

// ensureTLS provisions the server certificate, and the default user's
// client certificate, from the instance's certificate source before the
// ValkeyCluster references them. It returns a WaitError until the
// certificate of an issuer or of the user is there.
func ensureTLS(c *controller.Context) error {
	tls := tlsParameters(c.Instance())
	if tls.Mode == definition.TLSModeDisabled {
		return nil
	}
	deployed, err := c.Exists(&valkeyv1alpha1.ValkeyCluster{}, c.Name())
	if err != nil {
		return err
	}
	source := certificateSourceOf(tls)
	if source != tlsSourceIssuer && !deployed {
		// An issuer chosen, then dropped, before the instance was created.
		if err := deleteIssuedCertificates(c); err != nil {
			return err
		}
	}

	switch source {
	case tlsSourceIssuer:
		return ensureIssuedCertificates(c, tls, deployed)
	case tlsSourceSecret:
		found, err := c.Exists(&corev1.Secret{}, tls.CertificateSecretName)
		if err != nil || found {
			return err
		}
		return controller.WaitForDuration(
			fmt.Sprintf("waiting for the Secret %s of spec.parameters.tls.certificateSecretName", tls.CertificateSecretName),
			issuedCertificateWait)
	}
	if err := ensureTLSSecret(c, tls, deployed); err != nil {
		return err
	}
	return ensureClientCertSecret(c)
}

// ensureTLSSecret creates the instance CA and the server certificate when the
// TLS secret does not exist yet. An existing secret is left untouched so the
// certificate stays stable across reconciles and does not trigger pod rolls,
// until the additional names change: the server certificate is then reissued
// from the same CA, which on a running instance waits for maintenance
// approval, since the Valkey pods restart to present it.
func ensureTLSSecret(c *controller.Context, tls definition.TLSParameters, deployed bool) error {
	name := tlsSecretName(c.Name())
	want := namesHash(tls.AdditionalNames)

	existing := &corev1.Secret{}
	found, err := getOptional(c, existing, name)
	if err != nil {
		return err
	}
	if found {
		if managedNamesHash(existing) == want {
			return nil
		}
		return reissueServerCert(c, tls, deployed)
	}

	ca, err := newCertAuthority(c.Name())
	if err != nil {
		return fmt.Errorf("generating the CA: %w", err)
	}

	// The CA secret goes first: a missing TLS secret regenerates both, while a
	// TLS secret without its CA would block client certificates.
	caSecret := &corev1.Secret{
		ObjectMeta: c.ObjectMeta(tlsCASecretName(c.Name())),
		Type:       corev1.SecretTypeTLS,
		Data: map[string][]byte{
			tlsSecretKeyCA:   ca.certPEM,
			tlsSecretKeyCert: ca.certPEM,
			tlsSecretKeyKey:  ca.keyPEM(),
		},
	}
	if err := c.Apply(caSecret); err != nil {
		return err
	}
	return applyServerCert(c, ca, tls.AdditionalNames)
}

// reissueServerCert issues a server certificate for the current additional
// names from the instance CA. Instances without the CA key keep their
// certificate (validateTLSReferences reports it).
func reissueServerCert(c *controller.Context, tls definition.TLSParameters, deployed bool) error {
	caSecret := &corev1.Secret{}
	hasCA, err := getOptional(c, caSecret, tlsCASecretName(c.Name()))
	if err != nil || !hasCA {
		return err
	}
	if deployed && !c.RequestMaintenance("tls-names-"+namesHash(tls.AdditionalNames),
		"Reissue the server certificate for spec.parameters.tls.additionalNames and restart the Valkey pods to present it",
		controller.MaintenanceRollingRestart) {
		return nil
	}
	ca, err := parseCertAuthority(caSecret.Data[tlsSecretKeyCert], caSecret.Data[tlsSecretKeyKey])
	if err != nil {
		return fmt.Errorf("reading the CA from secret %q: %w", caSecret.Name, err)
	}
	return applyServerCert(c, ca, tls.AdditionalNames)
}

// applyServerCert writes a server certificate issued by ca for the
// instance's names and additionalNames, recording the latter's hash.
func applyServerCert(c *controller.Context, ca *certAuthority, additionalNames []string) error {
	certPEM, keyPEM, err := ca.issue(serverCertTemplate(c.Name(), c.Namespace(), additionalNames))
	if err != nil {
		return fmt.Errorf("issuing the server certificate: %w", err)
	}
	secret := &corev1.Secret{
		ObjectMeta: c.ObjectMeta(tlsSecretName(c.Name())),
		Type:       corev1.SecretTypeTLS,
		Data: map[string][]byte{
			tlsSecretKeyCA:   ca.certPEM,
			tlsSecretKeyCert: certPEM,
			tlsSecretKeyKey:  keyPEM,
		},
	}
	secret.Annotations = map[string]string{tlsNamesAnnotation: namesHash(additionalNames)}
	return c.Apply(secret)
}

// managedNamesHash returns the hash of the additional names the server
// certificate of the provider's CA was issued for. Certificates of releases
// before additionalNames have none.
func managedNamesHash(secret *corev1.Secret) string {
	if hash, ok := secret.Annotations[tlsNamesAnnotation]; ok {
		return hash
	}
	return namesHash(nil)
}

// tlsNamesPodAnnotation returns the hash of the additional names the server
// certificate the pods mount was issued for, which the pods carry so a
// reissue for other names restarts them. It is "" for the certificates of
// releases before additionalNames and for certificateSecretName, whose
// renewals Valkey picks up on its own (see tlsAutoReload).
func tlsNamesPodAnnotation(c *controller.Context, tls definition.TLSParameters) (string, error) {
	switch certificateSourceOf(tls) {
	case tlsSourceIssuer:
		vc := &valkeyv1alpha1.ValkeyCluster{}
		if _, err := getOptional(c, vc, c.Name()); err != nil {
			return "", err
		}
		var podHash string
		if vc.Spec.PodMetadata != nil {
			podHash = vc.Spec.PodMetadata.Annotations[tlsNamesAnnotation]
		}
		return issuedNamesHash(c, tls, podHash)
	case tlsSourceManaged:
		secret := &corev1.Secret{}
		if _, err := getOptional(c, secret, tlsSecretName(c.Name())); err != nil {
			return "", err
		}
		return secret.Annotations[tlsNamesAnnotation], nil
	}
	return "", nil
}

// getOptional reads the object called name, reporting whether it exists.
func getOptional(c *controller.Context, obj client.Object, name string) (bool, error) {
	if err := c.Get(obj, name); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// ensureClientCertSecret issues the default user's client certificate, signed
// by the instance CA, and re-issues it when the CA has changed. Instances
// without a CA key get none (validateTLSReferences rejects the settings that need one).
func ensureClientCertSecret(c *controller.Context) error {
	caSecret := &corev1.Secret{}
	if err := c.Get(caSecret, tlsCASecretName(c.Name())); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	ca, err := parseCertAuthority(caSecret.Data[tlsSecretKeyCert], caSecret.Data[tlsSecretKeyKey])
	if err != nil {
		return fmt.Errorf("reading the CA from secret %q: %w", caSecret.Name, err)
	}

	name := tlsClientSecretName(c.Name())
	existing := &corev1.Secret{}
	err = c.Get(existing, name)
	if err == nil && bytes.Equal(existing.Data[tlsSecretKeyCA], ca.certPEM) {
		return nil
	}
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	certPEM, keyPEM, err := ca.issue(clientCertTemplate(defaultUsername))
	if err != nil {
		return fmt.Errorf("issuing the client certificate: %w", err)
	}
	secret := &corev1.Secret{
		ObjectMeta: c.ObjectMeta(name),
		Type:       corev1.SecretTypeTLS,
		Data: map[string][]byte{
			tlsSecretKeyCA:   ca.certPEM,
			tlsSecretKeyCert: certPEM,
			tlsSecretKeyKey:  keyPEM,
		},
	}
	return c.Apply(secret)
}

// readCA returns the PEM-encoded CA certificate from the Secret the Valkey
// pods mount.
func readCA(c *controller.Context) ([]byte, error) {
	name := serverSecretName(c.Name(), tlsParameters(c.Instance()))
	secret := &corev1.Secret{}
	if err := c.Get(secret, name); err != nil {
		return nil, err
	}
	ca, ok := secret.Data[tlsSecretKeyCA]
	if !ok || len(ca) == 0 {
		return nil, fmt.Errorf("TLS secret %q is missing %q", name, tlsSecretKeyCA)
	}
	return ca, nil
}

// readClientCert returns the issued client certificate and key. ok is false
// when the instance has none.
func readClientCert(c *controller.Context) (certPEM, keyPEM []byte, ok bool, err error) {
	secret := &corev1.Secret{}
	if err := c.Get(secret, tlsClientSecretName(c.Name())); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil, false, nil
		}
		return nil, nil, false, err
	}
	certPEM, keyPEM = secret.Data[tlsSecretKeyCert], secret.Data[tlsSecretKeyKey]
	return certPEM, keyPEM, len(certPEM) > 0 && len(keyPEM) > 0, nil
}

// certAuthority is a self-signed CA that signs the instance's certificates.
type certAuthority struct {
	cert    *x509.Certificate
	key     *rsa.PrivateKey
	certPEM []byte
}

func newCertAuthority(instance string) (*certAuthority, error) {
	key, err := rsa.GenerateKey(rand.Reader, tlsRSABits)
	if err != nil {
		return nil, err
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: fmt.Sprintf("provider-valkey CA (%s)", instance)},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(tlsCertValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &certAuthority{
		cert:    cert,
		key:     key,
		certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
	}, nil
}

func parseCertAuthority(certPEM, keyPEM []byte) (*certAuthority, error) {
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		return nil, errors.New("no PEM certificate")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, err
	}
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, errors.New("no PEM private key")
	}
	key, err := x509.ParsePKCS1PrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, err
	}
	return &certAuthority{cert: cert, key: key, certPEM: certPEM}, nil
}

func (ca *certAuthority) keyPEM() []byte {
	return encodeRSAKey(ca.key)
}

// issue signs a certificate for a freshly generated key and returns both
// PEM-encoded. tmpl's SerialNumber and validity are filled in here.
func (ca *certAuthority) issue(tmpl *x509.Certificate) (certPEM, keyPEM []byte, err error) {
	key, err := rsa.GenerateKey(rand.Reader, tlsRSABits)
	if err != nil {
		return nil, nil, err
	}
	tmpl.SerialNumber, err = randomSerial()
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	tmpl.NotBefore = now.Add(-time.Hour)
	tmpl.NotAfter = now.Add(tlsCertValidity)

	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), encodeRSAKey(key), nil
}

// headlessServiceFQDN is the name of the operator's headless Service, which
// the operator verifies the nodes' certificates against.
func headlessServiceFQDN(instance, namespace string) string {
	return fmt.Sprintf("%s%s.%s.svc.cluster.local", headlessServicePrefix, instance, namespace)
}

// serverDNSNames are the names the server certificates of the provider's CA
// and of an issuer cover: the headless Service FQDN the operator verifies
// peers against, the per-pod names on that Service the nodes announce, the
// primary Service clients of the replication topology connect to, and
// localhost, which the probes and the metrics exporter connect to.
func serverDNSNames(instance, namespace string) []string {
	fqdn := headlessServiceFQDN(instance, namespace)
	return []string{fqdn, "*." + fqdn, primaryServiceFQDN(instance, namespace), "localhost"}
}

// serverCertTemplate covers serverDNSNames and the additional names.
func serverCertTemplate(instance, namespace string, additionalNames []string) *x509.Certificate {
	return &x509.Certificate{
		Subject:     pkix.Name{CommonName: headlessServiceFQDN(instance, namespace)},
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		DNSNames:    uniqueNames(serverDNSNames(instance, namespace), additionalNames),
	}
}

// serverCertCovers reports whether the instance's server certificate is valid
// for host. Certificates issued before a name was added do not cover it.
func serverCertCovers(c *controller.Context, host string) (bool, error) {
	name := serverSecretName(c.Name(), tlsParameters(c.Instance()))
	secret := &corev1.Secret{}
	if err := c.Get(secret, name); err != nil {
		return false, err
	}
	certs, err := parseCertificates(secret.Data[tlsSecretKeyCert])
	if err != nil {
		return false, fmt.Errorf("TLS secret %q: %w", name, err)
	}
	return certs[0].VerifyHostname(host) == nil, nil
}

// parseCertificates decodes the PEM certificates of data, at least one.
func parseCertificates(data []byte) ([]*x509.Certificate, error) {
	var certs []*x509.Certificate
	for {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, err
		}
		certs = append(certs, cert)
	}
	if len(certs) == 0 {
		return nil, errors.New("no PEM certificate")
	}
	return certs, nil
}

// clientCertTemplate names the ACL user in the Common Name, which Valkey maps
// to the user with tls-auth-clients-user CN.
func clientCertTemplate(user string) *x509.Certificate {
	return &x509.Certificate{
		Subject:     pkix.Name{CommonName: user},
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
}

func encodeRSAKey(key *rsa.PrivateKey) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}

func randomSerial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
}
