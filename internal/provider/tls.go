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
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	valkeyv1alpha1 "github.com/valkey-io/valkey-operator/api/v1alpha1"

	"github.com/openeverest/provider-valkey/definition/components"
	"github.com/openeverest/provider-valkey/internal/common"
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
	// the provider issues for the default user.
	tlsClientSecretSuffix = "-tls-client"

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
)

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

// engineTLSConfig returns the engine's TLS parameters, or the zero value when
// none are set.
func engineTLSConfig(c *controller.Context) components.ValkeyTLSConfig {
	engine, ok := c.Instance().Spec.Components[common.ComponentEngine]
	if !ok {
		return components.ValkeyTLSConfig{}
	}
	var cfg components.ValkeyEngineConfig
	if c.TryDecodeComponentParameters(engine, &cfg) && cfg.TLS != nil {
		return *cfg.TLS
	}
	return components.ValkeyTLSConfig{}
}

// tlsEnabled reports whether transport encryption is enabled for the instance.
// TLS is on by default and can be turned off with the engine component's
// tls.mode=disabled.
func tlsEnabled(c *controller.Context) bool {
	return engineTLSConfig(c).Mode != components.TLSModeDisabled
}

// clientCertificatesAccepted reports whether Valkey verifies the client
// certificates it is presented, which is what makes the issued one useful.
func clientCertificatesAccepted(cfg components.ValkeyTLSConfig) bool {
	return cfg.ClientAuth != components.TLSClientAuthDisabled
}

// clientCertificateNeeded reports whether clients cannot do without the
// issued client certificate.
func clientCertificateNeeded(cfg components.ValkeyTLSConfig) bool {
	return cfg.ClientAuth == components.TLSClientAuthRequired ||
		cfg.CertificateUser == components.TLSCertificateUserCN
}

// buildClientAuth maps the engine's TLS parameters onto the operator's
// clientAuth. It returns nil when neither is set, keeping the operator default.
func buildClientAuth(cfg components.ValkeyTLSConfig) *valkeyv1alpha1.TLSClientAuthSpec {
	if cfg.ClientAuth == "" && cfg.CertificateUser == "" {
		return nil
	}
	clientAuth := &valkeyv1alpha1.TLSClientAuthSpec{}
	switch cfg.ClientAuth {
	case components.TLSClientAuthRequired:
		clientAuth.Mode = valkeyv1alpha1.TLSAuthClientsRequired
	case components.TLSClientAuthDisabled:
		clientAuth.Mode = valkeyv1alpha1.TLSAuthClientsDisabled
	default:
		clientAuth.Mode = valkeyv1alpha1.TLSAuthClientsOptional
	}
	if cfg.CertificateUser == components.TLSCertificateUserCN {
		clientAuth.CertificateUser = valkeyv1alpha1.TLSAuthClientsUserCN
	} else {
		clientAuth.CertificateUser = valkeyv1alpha1.TLSAuthClientsUserDisabled
	}
	return clientAuth
}

// validateTLS rejects client authentication settings that cannot take effect.
func validateTLS(c *controller.Context) error {
	cfg := engineTLSConfig(c)
	if cfg.ClientAuth != components.TLSClientAuthRequired {
		if err := validateClientAuthNotRelaxed(c); err != nil {
			return err
		}
	}
	if !clientCertificateNeeded(cfg) {
		return nil
	}
	if !tlsEnabled(c) {
		if cfg.ClientAuth == components.TLSClientAuthRequired {
			return errors.New("tls.clientAuth=required needs transport encryption (tls.mode=enabled)")
		}
		return errors.New("tls.certificateUser=cn needs transport encryption (tls.mode=enabled)")
	}
	if cfg.ClientAuth == components.TLSClientAuthDisabled {
		return errors.New("tls.certificateUser=cn has no effect with tls.clientAuth=disabled")
	}
	if cfg.CertificateUser == components.TLSCertificateUserCN {
		if err := validateCertificateUserVersion(c); err != nil {
			return err
		}
	}

	// Instances created before the provider kept the CA key cannot have a
	// client certificate issued without replacing their CA.
	hasServerCert, err := c.Exists(&corev1.Secret{}, tlsSecretName(c.Name()))
	if err != nil || !hasServerCert {
		return err
	}
	hasCA, err := c.Exists(&corev1.Secret{}, tlsCASecretName(c.Name()))
	if err != nil || hasCA {
		return err
	}
	hasClientCert, err := c.Exists(&corev1.Secret{}, tlsClientSecretName(c.Name()))
	if err != nil || hasClientCert {
		return err
	}
	return fmt.Errorf("the instance has no CA key to issue client certificates with; "+
		"delete the %q secret to regenerate the TLS material, then restart the Valkey pods",
		tlsSecretName(c.Name()))
}

// validateClientAuthNotRelaxed rejects moving a running cluster off required
// client certificates. valkey-operator v0.7.1 stops presenting its own client
// certificate as soon as the spec no longer requires one, which locks it out
// of the nodes that have not rolled yet and stalls the cluster.
func validateClientAuthNotRelaxed(c *controller.Context) error {
	vc := &valkeyv1alpha1.ValkeyCluster{}
	if err := c.Get(vc, c.Name()); err != nil {
		return client.IgnoreNotFound(err)
	}
	if vc.GetTLS().ClientAuthMode() == valkeyv1alpha1.TLSAuthClientsRequired {
		return errors.New("tls.clientAuth cannot be changed from required on a running instance: " +
			"valkey-operator would lose access to the nodes that still require client certificates")
	}
	return nil
}

// validateCertificateUserVersion rejects certificate-to-user mapping on Valkey
// versions without it. A custom image has no known version and is accepted.
func validateCertificateUserVersion(c *controller.Context) error {
	version, known, err := engineVersion(c)
	if err != nil || !known {
		return err
	}
	major, err := strconv.Atoi(strings.SplitN(version, ".", 2)[0])
	if err != nil {
		return nil
	}
	if major < certificateUserMinMajorVersion {
		return fmt.Errorf("tls.certificateUser=cn requires Valkey %d.0 or later, the instance runs %s",
			certificateUserMinMajorVersion, version)
	}
	return nil
}

// engineVersion returns the Valkey version the instance runs, resolved the way
// the runtime does before Sync. known is false for a custom image.
func engineVersion(c *controller.Context) (version string, known bool, err error) {
	engine := c.Instance().Spec.Components[common.ComponentEngine]
	if engine.Image != "" {
		return "", false, nil
	}
	if engine.Version != "" {
		return engine.Version, true, nil
	}

	spec, err := c.ProviderSpec()
	if err != nil {
		return "", false, err
	}
	if name := controller.EffectiveVersionBundleName(spec, c.Instance()); name != "" {
		bundle, err := controller.ResolveVersionBundle(spec, name)
		if err != nil {
			return "", false, err
		}
		if v := bundle.Components[common.ComponentEngine]; v != "" {
			return v, true, nil
		}
	}
	if ct, ok := spec.ComponentTypes[controller.GetComponentType(spec, common.ComponentEngine)]; ok && ct.DefaultVersion != "" {
		return ct.DefaultVersion, true, nil
	}
	return "", false, nil
}

// ensureTLSSecret creates the instance CA and the server certificate when the
// TLS secret does not exist yet. An existing secret is left untouched so the
// certificate stays stable across reconciles and does not trigger pod rolls.
func ensureTLSSecret(c *controller.Context) error {
	name := tlsSecretName(c.Name())

	exists, err := c.Exists(&corev1.Secret{}, name)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	ca, err := newCertAuthority(c.Name())
	if err != nil {
		return fmt.Errorf("generating the CA: %w", err)
	}
	certPEM, keyPEM, err := ca.issue(serverCertTemplate(c.Name(), c.Namespace()))
	if err != nil {
		return fmt.Errorf("issuing the server certificate: %w", err)
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

// ensureClientCertSecret issues the default user's client certificate, signed
// by the instance CA, and re-issues it when the CA has changed. Instances
// without a CA key get none (validateTLS rejects the settings that need one).
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

// readCA returns the PEM-encoded CA certificate from the instance's TLS secret.
func readCA(c *controller.Context) ([]byte, error) {
	secret := &corev1.Secret{}
	if err := c.Get(secret, tlsSecretName(c.Name())); err != nil {
		return nil, err
	}
	ca, ok := secret.Data[tlsSecretKeyCA]
	if !ok || len(ca) == 0 {
		return nil, fmt.Errorf("TLS secret %q is missing %q", tlsSecretName(c.Name()), tlsSecretKeyCA)
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

// serverCertTemplate covers the headless Service FQDN the operator verifies
// peers against, and the per-pod names on that Service.
func serverCertTemplate(instance, namespace string) *x509.Certificate {
	fqdn := fmt.Sprintf("%s%s.%s.svc.cluster.local", headlessServicePrefix, instance, namespace)
	return &x509.Certificate{
		Subject:     pkix.Name{CommonName: fqdn},
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		DNSNames:    []string{fqdn, "*." + fqdn, "localhost"},
	}
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
