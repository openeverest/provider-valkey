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
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"slices"
	"testing"

	valkeyv1alpha1 "github.com/valkey-io/valkey-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/openeverest/provider-valkey/definition/components"
)

func TestCertAuthorityIssuesServerCertificate(t *testing.T) {
	ca, err := newCertAuthority("mycache")
	if err != nil {
		t.Fatalf("newCertAuthority: %v", err)
	}
	certPEM, keyPEM, err := ca.issue(serverCertTemplate("mycache", "team-a"))
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
		t.Fatalf("server cert/key do not form a valid pair: %v", err)
	}

	serverCert := parseCert(t, certPEM)
	roots := certPool(t, ca.certPEM)

	// The operator verifies peers against the headless Service FQDN, and nodes
	// announce <pod>.<headless FQDN> with hostname discovery.
	for _, name := range []string{
		"valkey-mycache.team-a.svc.cluster.local",
		"valkey-mycache-0-0.valkey-mycache.team-a.svc.cluster.local",
	} {
		if _, err := serverCert.Verify(x509.VerifyOptions{DNSName: name, Roots: roots}); err != nil {
			t.Errorf("server cert not verifiable for %q: %v", name, err)
		}
	}
}

func TestCertAuthorityIssuesClientCertificateAfterReload(t *testing.T) {
	original, err := newCertAuthority("mycache")
	if err != nil {
		t.Fatalf("newCertAuthority: %v", err)
	}
	// Client certificates are signed with the CA read back from its secret.
	ca, err := parseCertAuthority(original.certPEM, original.keyPEM())
	if err != nil {
		t.Fatalf("parseCertAuthority: %v", err)
	}

	certPEM, keyPEM, err := ca.issue(clientCertTemplate(defaultUsername))
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
		t.Fatalf("client cert/key do not form a valid pair: %v", err)
	}

	clientCert := parseCert(t, certPEM)
	if clientCert.Subject.CommonName != defaultUsername {
		t.Errorf("client cert CN = %q, want %q", clientCert.Subject.CommonName, defaultUsername)
	}
	if _, err := clientCert.Verify(x509.VerifyOptions{
		Roots:     certPool(t, original.certPEM),
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		t.Fatalf("client cert not verifiable for client auth against the CA: %v", err)
	}
}

func TestBuildClientAuth(t *testing.T) {
	tests := []struct {
		name string
		cfg  components.ValkeyTLSConfig
		want *valkeyv1alpha1.TLSClientAuthSpec
	}{
		{
			name: "unset keeps the operator default",
		},
		{
			name: "required enforces mTLS",
			cfg:  components.ValkeyTLSConfig{ClientAuth: components.TLSClientAuthRequired},
			want: &valkeyv1alpha1.TLSClientAuthSpec{
				Mode:            valkeyv1alpha1.TLSAuthClientsRequired,
				CertificateUser: valkeyv1alpha1.TLSAuthClientsUserDisabled,
			},
		},
		{
			name: "certificate user alone keeps client certificates optional",
			cfg:  components.ValkeyTLSConfig{CertificateUser: components.TLSCertificateUserCN},
			want: &valkeyv1alpha1.TLSClientAuthSpec{
				Mode:            valkeyv1alpha1.TLSAuthClientsOptional,
				CertificateUser: valkeyv1alpha1.TLSAuthClientsUserCN,
			},
		},
		{
			name: "disabled ignores client certificates",
			cfg:  components.ValkeyTLSConfig{ClientAuth: components.TLSClientAuthDisabled},
			want: &valkeyv1alpha1.TLSClientAuthSpec{
				Mode:            valkeyv1alpha1.TLSAuthClientsDisabled,
				CertificateUser: valkeyv1alpha1.TLSAuthClientsUserDisabled,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := buildClientAuth(tt.cfg); !equality.Semantic.DeepEqual(got, tt.want) {
				t.Errorf("buildClientAuth() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestValidateTLSReferences(t *testing.T) {
	legacyTLSSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: tlsSecretName(testInstance), Namespace: testNamespace}}
	caSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: tlsCASecretName(testInstance), Namespace: testNamespace}}
	clientSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: tlsClientSecretName(testInstance), Namespace: testNamespace}}
	requiredCluster := &valkeyv1alpha1.ValkeyCluster{
		ObjectMeta: metav1.ObjectMeta{Name: testInstance, Namespace: testNamespace},
		Spec: valkeyv1alpha1.ValkeyClusterSpec{Networking: &valkeyv1alpha1.NetworkingSpec{TLS: &valkeyv1alpha1.TLSSpec{
			ClientAuth: &valkeyv1alpha1.TLSClientAuthSpec{Mode: valkeyv1alpha1.TLSAuthClientsRequired},
		}}},
	}

	tests := []struct {
		name       string
		parameters string
		objs       []client.Object
		want       []string
	}{
		{
			name:       "required client auth on a new instance",
			parameters: `{"tls":{"clientAuth":"required"}}`,
		},
		{
			name:       "required client auth on an instance without a CA key",
			parameters: `{"tls":{"clientAuth":"required"}}`,
			objs:       []client.Object{legacyTLSSecret},
			want:       []string{"spec.components[engine].parameters.tls.clientAuth"},
		},
		{
			name:       "certificate user on an instance without a CA key",
			parameters: `{"tls":{"certificateUser":"cn"}}`,
			objs:       []client.Object{legacyTLSSecret},
			want:       []string{"spec.components[engine].parameters.tls.certificateUser"},
		},
		{
			name:       "required client auth on an instance with a CA key",
			parameters: `{"tls":{"clientAuth":"required"}}`,
			objs:       []client.Object{legacyTLSSecret, caSecret},
		},
		{
			name:       "required client auth keeps an already issued client certificate",
			parameters: `{"tls":{"clientAuth":"required"}}`,
			objs:       []client.Object{legacyTLSSecret, clientSecret},
		},
		{
			name:       "running cluster keeps requiring client certificates",
			parameters: `{"tls":{"clientAuth":"required","certificateUser":"cn"}}`,
			objs:       []client.Object{requiredCluster},
		},
		{
			name:       "running cluster cannot stop requiring client certificates",
			parameters: `{"tls":{"clientAuth":"optional"}}`,
			objs:       []client.Object{requiredCluster},
			want:       []string{"spec.components[engine].parameters.tls.clientAuth"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newTestContext(t, newTestInstance(tt.parameters, false), interceptor.Funcs{}, tt.objs...)
			errs, err := validateTLSReferences(c)
			if err != nil {
				t.Fatalf("validateTLSReferences: %v", err)
			}
			var got []string
			for _, e := range errs {
				got = append(got, e.Field)
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("validateTLSReferences() = %v, want errors on %v", errs, tt.want)
			}
		})
	}
}

func TestSyncIssuesClientCertificate(t *testing.T) {
	in := newTestInstance(`{"tls":{"clientAuth":"required","certificateUser":"cn"}}`, false)
	c, cl := newTestContext(t, in, interceptor.Funcs{})
	p := New()

	if err := p.Sync(c); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	vc := &valkeyv1alpha1.ValkeyCluster{}
	if err := cl.Get(context.Background(), client.ObjectKey{Namespace: testNamespace, Name: testInstance}, vc); err != nil {
		t.Fatalf("getting ValkeyCluster: %v", err)
	}
	if got := vc.GetTLS().ClientAuthMode(); got != valkeyv1alpha1.TLSAuthClientsRequired {
		t.Errorf("clientAuth.mode = %q, want Required", got)
	}
	if got := vc.GetTLS().ClientAuthCertificateUser(); got != valkeyv1alpha1.TLSAuthClientsUserCN {
		t.Errorf("clientAuth.certificateUser = %q, want CN", got)
	}

	assertClientCertTrusted(t, cl)

	// Deleting the TLS secret regenerates the CA; the client certificate must
	// follow it.
	if err := cl.Delete(context.Background(), getSecret(t, cl, tlsSecretName(testInstance))); err != nil {
		t.Fatal(err)
	}
	oldClientCert := getSecret(t, cl, tlsClientSecretName(testInstance)).Data[tlsSecretKeyCert]
	if err := p.Sync(c); err != nil {
		t.Fatalf("second Sync: %v", err)
	}
	if string(getSecret(t, cl, tlsClientSecretName(testInstance)).Data[tlsSecretKeyCert]) == string(oldClientCert) {
		t.Fatal("client certificate was not re-issued for the new CA")
	}
	assertClientCertTrusted(t, cl)
}

func TestConnectionDetailsClientCertificate(t *testing.T) {
	tests := []struct {
		name       string
		parameters string
		wantCert   bool
	}{
		{name: "published by default", wantCert: true},
		{name: "published when required", parameters: `{"tls":{"clientAuth":"required"}}`, wantCert: true},
		{name: "omitted when client certificates are ignored", parameters: `{"tls":{"clientAuth":"disabled"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newTestContext(t, newTestInstance(tt.parameters, false), interceptor.Funcs{})
			if err := New().Sync(c); err != nil {
				t.Fatalf("Sync: %v", err)
			}

			cd, err := buildConnectionDetails(c)
			if err != nil {
				t.Fatalf("buildConnectionDetails: %v", err)
			}
			_, hasCert := cd.AdditionalProperties[tlsSecretKeyCert]
			_, hasKey := cd.AdditionalProperties[tlsSecretKeyKey]
			if hasCert != tt.wantCert || hasKey != tt.wantCert {
				t.Fatalf("client cert published = %v/%v, want %v", hasCert, hasKey, tt.wantCert)
			}
		})
	}
}

// assertClientCertTrusted checks that the issued client certificate verifies
// against the CA the Valkey pods trust.
func assertClientCertTrusted(t *testing.T, cl client.Client) {
	t.Helper()
	serverSecret := getSecret(t, cl, tlsSecretName(testInstance))
	clientSecret := getSecret(t, cl, tlsClientSecretName(testInstance))

	clientCert := parseCert(t, clientSecret.Data[tlsSecretKeyCert])
	if _, err := clientCert.Verify(x509.VerifyOptions{
		Roots:     certPool(t, serverSecret.Data[tlsSecretKeyCA]),
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		t.Fatalf("client cert not trusted by the server CA: %v", err)
	}
}

func certPool(t *testing.T, caPEM []byte) *x509.CertPool {
	t.Helper()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatal("failed to parse CA certificate")
	}
	return pool
}

func parseCert(t *testing.T, certPEM []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("failed to decode certificate PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("failed to parse certificate: %v", err)
	}
	return cert
}
