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
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-valkey/definition/components"
	"github.com/openeverest/provider-valkey/internal/common"
)

const (
	// tlsSecretSuffix is appended to the Instance name to form the TLS secret.
	tlsSecretSuffix = "-tls"

	// tlsCertValidity is the lifetime of the self-signed certificates. The
	// provider does not rotate them yet, so it is deliberately long-lived.
	tlsCertValidity = 10 * 365 * 24 * time.Hour

	// tlsRSABits is the RSA key size for the generated CA and server keys.
	tlsRSABits = 2048

	tlsSecretKeyCA   = "ca.crt"
	tlsSecretKeyCert = "tls.crt"
	tlsSecretKeyKey  = "tls.key"
)

// tlsSecretName returns the name of the TLS secret for the given instance.
func tlsSecretName(instance string) string {
	return instance + tlsSecretSuffix
}

// tlsEnabled reports whether transport encryption is enabled for the instance.
// TLS is on by default and can be turned off with the engine component's
// tls.enabled=false.
func tlsEnabled(c *controller.Context) bool {
	engine, ok := c.Instance().Spec.Components[common.ComponentEngine]
	if !ok {
		return true
	}
	var cfg components.ValkeyEngineConfig
	if c.TryDecodeComponentParameters(engine, &cfg) && cfg.TLS != nil && cfg.TLS.Enabled != nil {
		return *cfg.TLS.Enabled
	}
	return true
}

// ensureTLSSecret creates the self-signed TLS secret for the instance when it
// does not already exist. An existing secret is left untouched so the
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

	caPEM, certPEM, keyPEM, err := generateTLSMaterial(c.Name(), c.Namespace())
	if err != nil {
		return fmt.Errorf("generating TLS material: %w", err)
	}

	secret := &corev1.Secret{
		ObjectMeta: c.ObjectMeta(name),
		Type:       corev1.SecretTypeTLS,
		Data: map[string][]byte{
			tlsSecretKeyCA:   caPEM,
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

// generateTLSMaterial creates a self-signed CA and a server certificate signed
// by it, valid for the cluster's headless Service DNS names. It returns the
// PEM-encoded ca.crt, tls.crt, and tls.key.
func generateTLSMaterial(instance, namespace string) (caPEM, certPEM, keyPEM []byte, err error) {
	now := time.Now()

	caKey, err := rsa.GenerateKey(rand.Reader, tlsRSABits)
	if err != nil {
		return nil, nil, nil, err
	}
	caSerial, err := randomSerial()
	if err != nil {
		return nil, nil, nil, err
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          caSerial,
		Subject:               pkix.Name{CommonName: fmt.Sprintf("provider-valkey CA (%s)", instance)},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(tlsCertValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, nil, err
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, nil, nil, err
	}

	serverKey, err := rsa.GenerateKey(rand.Reader, tlsRSABits)
	if err != nil {
		return nil, nil, nil, err
	}
	serverSerial, err := randomSerial()
	if err != nil {
		return nil, nil, nil, err
	}
	// The operator verifies peers against the headless Service FQDN; the
	// wildcard covers per-pod DNS names on that Service.
	fqdn := fmt.Sprintf("%s%s.%s.svc.cluster.local", headlessServicePrefix, instance, namespace)
	serverTmpl := &x509.Certificate{
		SerialNumber: serverSerial,
		Subject:      pkix.Name{CommonName: fqdn},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(tlsCertValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		DNSNames:     []string{fqdn, "*." + fqdn, "localhost"},
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTmpl, caCert, &serverKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, nil, err
	}

	caPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(serverKey)})
	return caPEM, certPEM, keyPEM, nil
}

func randomSerial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
}
