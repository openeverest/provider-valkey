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

	cmapi "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	cmmeta "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	valkeyv1alpha1 "github.com/valkey-io/valkey-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/openeverest/provider-valkey/definition"
	"github.com/openeverest/provider-valkey/internal/common"
)

func TestCertAuthorityIssuesServerCertificate(t *testing.T) {
	ca, err := newCertAuthority("mycache")
	if err != nil {
		t.Fatalf("newCertAuthority: %v", err)
	}
	certPEM, keyPEM, err := ca.issue(serverCertTemplate("mycache", "team-a", nil))
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
		cfg  definition.TLSParameters
		want *valkeyv1alpha1.TLSClientAuthSpec
	}{
		{
			name: "unset keeps the operator default",
		},
		{
			name: "required enforces mTLS",
			cfg:  definition.TLSParameters{ClientAuth: definition.TLSClientAuthRequired},
			want: &valkeyv1alpha1.TLSClientAuthSpec{
				Mode:            valkeyv1alpha1.TLSAuthClientsRequired,
				CertificateUser: valkeyv1alpha1.TLSAuthClientsUserDisabled,
			},
		},
		{
			name: "certificate user alone keeps client certificates optional",
			cfg:  definition.TLSParameters{CertificateUser: definition.TLSCertificateUserCN},
			want: &valkeyv1alpha1.TLSClientAuthSpec{
				Mode:            valkeyv1alpha1.TLSAuthClientsOptional,
				CertificateUser: valkeyv1alpha1.TLSAuthClientsUserCN,
			},
		},
		{
			name: "disabled ignores client certificates",
			cfg:  definition.TLSParameters{ClientAuth: definition.TLSClientAuthDisabled},
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
			Certificates: valkeyv1alpha1.TLSCertificates{Server: valkeyv1alpha1.CertificateSource{SecretName: tlsSecretName(testInstance)}},
			ClientAuth:   &valkeyv1alpha1.TLSClientAuthSpec{Mode: valkeyv1alpha1.TLSAuthClientsRequired},
		}}},
	}

	issuedCluster := requiredCluster.DeepCopy()
	issuedCluster.Spec.Networking.TLS.Certificates.Server.SecretName = tlsIssuedSecretName(testInstance)
	issuedCluster.Spec.Networking.TLS.ClientAuth = nil
	plainCluster := &valkeyv1alpha1.ValkeyCluster{ObjectMeta: metav1.ObjectMeta{Name: testInstance, Namespace: testNamespace}}
	issuedCertificate := &cmapi.Certificate{
		ObjectMeta: metav1.ObjectMeta{Name: tlsIssuedSecretName(testInstance), Namespace: testNamespace},
		Spec:       cmapi.CertificateSpec{IssuerRef: cmmeta.IssuerReference{Name: "company-ca"}},
	}
	userCert := func(mutate func(*x509.Certificate)) *corev1.Secret {
		tmpl := serverCertTemplate(testInstance, testNamespace, nil)
		if mutate != nil {
			mutate(tmpl)
		}
		return serverSecret(t, "cache-cert", tmpl)
	}
	userSecret := `{"tls":{"certificateSecretName":"cache-cert"}}`

	tests := []struct {
		name       string
		parameters string
		monitoring bool
		objs       []client.Object
		want       []string
	}{
		{
			name:       "TLS cannot be turned off on a running cluster",
			parameters: `{"tls":{"mode":"disabled"}}`,
			objs:       []client.Object{requiredCluster},
			want:       []string{"spec.parameters.tls.mode"},
		},
		{
			name: "TLS cannot be turned on on a running cluster",
			objs: []client.Object{plainCluster},
			want: []string{"spec.parameters.tls.mode"},
		},
		{
			name:       "the certificate source cannot change on a running cluster",
			parameters: `{"tls":{"clientAuth":"required","issuer":{"name":"company-ca"}}}`,
			objs:       []client.Object{requiredCluster},
			want:       []string{"spec.parameters.tls"},
		},
		{
			name:       "running cluster keeps its issuer",
			parameters: `{"tls":{"issuer":{"name":"company-ca","kind":"Issuer"}}}`,
			objs:       []client.Object{issuedCluster, issuedCertificate},
		},
		{
			name:       "running cluster cannot change its issuer",
			parameters: `{"tls":{"issuer":{"name":"company-ca","kind":"ClusterIssuer"}}}`,
			objs:       []client.Object{issuedCluster, issuedCertificate},
			want:       []string{"spec.parameters.tls.issuer"},
		},
		{
			name:       "certificate of the user",
			parameters: userSecret,
			objs:       []client.Object{userCert(nil)},
		},
		{
			name:       "certificate of the user is not there yet",
			parameters: userSecret,
		},
		{
			name:       "certificate of the user not valid for the pods",
			parameters: userSecret,
			objs: []client.Object{userCert(func(c *x509.Certificate) {
				c.DNSNames = []string{headlessServiceFQDN(testInstance, testNamespace)}
			})},
			want: []string{"spec.parameters.tls.certificateSecretName"},
		},
		{
			name:       "certificate of the user without localhost, for the metrics exporter",
			parameters: userSecret,
			monitoring: true,
			objs: []client.Object{userCert(func(c *x509.Certificate) {
				c.DNSNames = slices.DeleteFunc(c.DNSNames, func(name string) bool { return name == "localhost" })
			})},
			want: []string{"spec.parameters.tls.certificateSecretName"},
		},
		{
			name:       "certificate of the user without localhost, without the metrics exporter",
			parameters: userSecret,
			objs: []client.Object{userCert(func(c *x509.Certificate) {
				c.DNSNames = slices.DeleteFunc(c.DNSNames, func(name string) bool { return name == "localhost" })
			})},
		},
		{
			name:       "certificate of the user only for servers",
			parameters: userSecret,
			objs: []client.Object{userCert(func(c *x509.Certificate) {
				c.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
			})},
			want: []string{"spec.parameters.tls.certificateSecretName"},
		},
		{
			name:       "additional names on an instance without a CA key",
			parameters: `{"tls":{"additionalNames":["cache.example.com"]}}`,
			objs:       []client.Object{legacyTLSSecret},
			want:       []string{"spec.parameters.tls.additionalNames"},
		},
		{
			name:       "required client auth on a new instance",
			parameters: `{"tls":{"clientAuth":"required"}}`,
		},
		{
			name:       "required client auth on an instance without a CA key",
			parameters: `{"tls":{"clientAuth":"required"}}`,
			objs:       []client.Object{legacyTLSSecret},
			want:       []string{"spec.parameters.tls.clientAuth"},
		},
		{
			name:       "certificate user on an instance without a CA key",
			parameters: `{"tls":{"certificateUser":"cn"}}`,
			objs:       []client.Object{legacyTLSSecret},
			want:       []string{"spec.parameters.tls.certificateUser"},
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
			want:       []string{"spec.parameters.tls.clientAuth"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newTestContext(t, newTestInstance(tt.parameters, tt.monitoring), interceptor.Funcs{}, tt.objs...)
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

// serverSecret returns a certificate Secret of name, issued by a new CA from
// tmpl.
func serverSecret(t *testing.T, name string, tmpl *x509.Certificate) *corev1.Secret {
	t.Helper()
	ca, err := newCertAuthority(testInstance)
	if err != nil {
		t.Fatal(err)
	}
	certPEM, keyPEM, err := ca.issue(tmpl)
	if err != nil {
		t.Fatal(err)
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Type:       corev1.SecretTypeTLS,
		Data:       map[string][]byte{tlsSecretKeyCA: ca.certPEM, tlsSecretKeyCert: certPEM, tlsSecretKeyKey: keyPEM},
	}
}

func getValkeyCluster(t *testing.T, cl client.Client) *valkeyv1alpha1.ValkeyCluster {
	t.Helper()
	vc := &valkeyv1alpha1.ValkeyCluster{}
	if err := cl.Get(context.Background(), client.ObjectKey{Namespace: testNamespace, Name: testInstance}, vc); err != nil {
		t.Fatalf("getting ValkeyCluster: %v", err)
	}
	return vc
}

func TestSyncIssuerCertificates(t *testing.T) {
	names := []string{"cache.example.com"}
	tests := []struct {
		name       string
		version    string
		wantReload bool
	}{
		{name: "Valkey 9.0 cannot reload the certificate"},
		{name: "Valkey 9.1 reloads the renewed certificate", version: "9.1.2", wantReload: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := newTestInstance(`{"tls":{"issuer":{"name":"company-ca","kind":"ClusterIssuer"},"additionalNames":["cache.example.com"]}}`, false)
			engine := in.Spec.Components["engine"]
			engine.Version = tt.version
			in.Spec.Components["engine"] = engine
			c, cl := newTestContext(t, in, interceptor.Funcs{})
			p := New()

			if err := p.Sync(c); !controller.IsWaitError(err) {
				t.Fatalf("Sync before the certificate is issued = %v, want a wait", err)
			}
			if found, _ := c.Exists(&valkeyv1alpha1.ValkeyCluster{}, testInstance); found {
				t.Fatal("ValkeyCluster created before the server certificate is issued")
			}

			server := &cmapi.Certificate{}
			if err := cl.Get(context.Background(), client.ObjectKey{Namespace: testNamespace, Name: tlsIssuedSecretName(testInstance)}, server); err != nil {
				t.Fatalf("getting the server Certificate: %v", err)
			}
			wantRef := cmmeta.IssuerReference{Name: "company-ca", Kind: "ClusterIssuer", Group: certManagerGroup}
			if server.Spec.IssuerRef != wantRef {
				t.Errorf("server issuerRef = %+v, want %+v", server.Spec.IssuerRef, wantRef)
			}
			for _, name := range []string{headlessServiceFQDN(testInstance, testNamespace), "cache.example.com"} {
				if !slices.Contains(server.Spec.DNSNames, name) {
					t.Errorf("server dnsNames = %v, missing %q", server.Spec.DNSNames, name)
				}
			}
			if !slices.Contains(server.Spec.Usages, cmapi.UsageClientAuth) {
				t.Errorf("server usages = %v, want client auth for the cluster bus", server.Spec.Usages)
			}
			clientCert := &cmapi.Certificate{}
			if err := cl.Get(context.Background(), client.ObjectKey{Namespace: testNamespace, Name: tlsClientSecretName(testInstance)}, clientCert); err != nil {
				t.Fatalf("getting the client Certificate: %v", err)
			}
			if clientCert.Spec.CommonName != defaultUsername || clientCert.Spec.IssuerRef != wantRef {
				t.Errorf("client Certificate = %+v", clientCert.Spec)
			}

			// cert-manager writes the Secret.
			issued := serverSecret(t, tlsIssuedSecretName(testInstance), serverCertTemplate(testInstance, testNamespace, names))
			if err := cl.Create(context.Background(), issued); err != nil {
				t.Fatal(err)
			}
			if err := p.Sync(c); err != nil {
				t.Fatalf("Sync: %v", err)
			}
			vc := getValkeyCluster(t, cl)
			if got := vc.GetTLS().Certificates.Server.SecretName; got != tlsIssuedSecretName(testInstance) {
				t.Errorf("server secretName = %q, want %q", got, tlsIssuedSecretName(testInstance))
			}
			if got := vc.Spec.PodMetadata.Annotations[tlsNamesAnnotation]; got != namesHash(names) {
				t.Errorf("pod names annotation = %q, want %q", got, namesHash(names))
			}
			if _, got := vc.Spec.Config[tlsAutoReloadConfig]; got != tt.wantReload {
				t.Errorf("%s set = %v, want %v", tlsAutoReloadConfig, got, tt.wantReload)
			}
			if found, _ := c.Exists(&corev1.Secret{}, tlsSecretName(testInstance)); found {
				t.Error("the provider CA issued a certificate for an instance of an issuer")
			}

			// New names on the running instance, approved: the pods restart
			// only once cert-manager has reissued the certificate, and Sync
			// waits for it, as nothing else triggers it.
			newNames := []string{"cache.example.com", "*.cache.example.com"}
			in.Spec.Parameters.Raw = []byte(`{"tls":{"issuer":{"name":"company-ca","kind":"ClusterIssuer"},"additionalNames":["cache.example.com","*.cache.example.com"]}}`)
			in.Spec.Maintenance = &corev1alpha1.MaintenanceSpec{Approved: "tls-names-" + namesHash(newNames)}
			c = controller.NewContext(context.Background(), cl, in, common.ProviderName)
			if err := p.Sync(c); !controller.IsWaitError(err) {
				t.Fatalf("Sync before the certificate is reissued = %v, want a wait", err)
			}
			if got := getValkeyCluster(t, cl).Spec.PodMetadata.Annotations[tlsNamesAnnotation]; got != namesHash(names) {
				t.Fatalf("pods restarted before the certificate was reissued: annotation %q", got)
			}
			if err := cl.Delete(context.Background(), issued); err != nil {
				t.Fatal(err)
			}
			if err := cl.Create(context.Background(), serverSecret(t, tlsIssuedSecretName(testInstance), serverCertTemplate(testInstance, testNamespace, newNames))); err != nil {
				t.Fatal(err)
			}
			if err := p.Sync(c); err != nil {
				t.Fatalf("Sync: %v", err)
			}
			if got := getValkeyCluster(t, cl).Spec.PodMetadata.Annotations[tlsNamesAnnotation]; got != namesHash(newNames) {
				t.Errorf("pod names annotation = %q, want %q to restart the pods", got, namesHash(newNames))
			}
		})
	}
}

func TestSyncCertificateSecretOfUser(t *testing.T) {
	c, cl := newTestContext(t, newTestInstance(`{"tls":{"certificateSecretName":"cache-cert"}}`, false), interceptor.Funcs{})
	p := New()

	if err := p.Sync(c); !controller.IsWaitError(err) {
		t.Fatalf("Sync before the Secret exists = %v, want a wait", err)
	}
	if err := cl.Create(context.Background(), serverSecret(t, "cache-cert", serverCertTemplate(testInstance, testNamespace, nil))); err != nil {
		t.Fatal(err)
	}
	if err := p.Sync(c); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if got := getValkeyCluster(t, cl).GetTLS().Certificates.Server.SecretName; got != "cache-cert" {
		t.Errorf("server secretName = %q, want cache-cert", got)
	}
	for _, name := range []string{tlsSecretName(testInstance), tlsClientSecretName(testInstance)} {
		if found, _ := c.Exists(&corev1.Secret{}, name); found {
			t.Errorf("the provider CA issued the Secret %s for an instance of the user's certificate", name)
		}
	}
}

func TestSyncManagedAdditionalNames(t *testing.T) {
	const parameters = `{"tls":{"additionalNames":["cache.example.com"]}}`
	names := []string{"cache.example.com"}

	t.Run("new instance", func(t *testing.T) {
		c, cl := newTestContext(t, newTestInstance(parameters, false), interceptor.Funcs{})
		if err := New().Sync(c); err != nil {
			t.Fatalf("Sync: %v", err)
		}
		if err := parseCert(t, getSecret(t, cl, tlsSecretName(testInstance)).Data[tlsSecretKeyCert]).VerifyHostname("cache.example.com"); err != nil {
			t.Errorf("server certificate: %v", err)
		}
		if got := getValkeyCluster(t, cl).Spec.PodMetadata.Annotations[tlsNamesAnnotation]; got != namesHash(names) {
			t.Errorf("pod names annotation = %q, want %q", got, namesHash(names))
		}
	})

	t.Run("running instance waits for maintenance approval", func(t *testing.T) {
		in := newTestInstance("", false)
		c, cl := newTestContext(t, in, interceptor.Funcs{})
		if err := New().Sync(c); err != nil {
			t.Fatalf("Sync: %v", err)
		}
		before := getSecret(t, cl, tlsSecretName(testInstance))
		podHash := getValkeyCluster(t, cl).Spec.PodMetadata.Annotations[tlsNamesAnnotation]

		in = newTestInstance(parameters, false)
		c = controller.NewContext(context.Background(), cl, in, common.ProviderName)
		if err := New().Sync(c); err != nil {
			t.Fatalf("Sync: %v", err)
		}
		token := "tls-names-" + namesHash(names)
		pending := c.GetPendingMaintenance()
		if len(pending) != 1 || pending[0].ApprovalToken != token {
			t.Fatalf("pending maintenance = %+v, want %s", pending, token)
		}
		if got := getSecret(t, cl, tlsSecretName(testInstance)).Data[tlsSecretKeyCert]; string(got) != string(before.Data[tlsSecretKeyCert]) {
			t.Fatal("server certificate reissued without approval")
		}
		if got := getValkeyCluster(t, cl).Spec.PodMetadata.Annotations[tlsNamesAnnotation]; got != podHash {
			t.Fatalf("pods restarted without approval: annotation %q, was %q", got, podHash)
		}

		in.Spec.Maintenance = &corev1alpha1.MaintenanceSpec{Approved: token}
		c = controller.NewContext(context.Background(), cl, in, common.ProviderName)
		if err := New().Sync(c); err != nil {
			t.Fatalf("Sync: %v", err)
		}
		after := getSecret(t, cl, tlsSecretName(testInstance))
		if string(after.Data[tlsSecretKeyCA]) != string(before.Data[tlsSecretKeyCA]) {
			t.Error("reissuing the server certificate replaced the CA")
		}
		if err := parseCert(t, after.Data[tlsSecretKeyCert]).VerifyHostname("cache.example.com"); err != nil {
			t.Errorf("reissued server certificate: %v", err)
		}
		if got := getValkeyCluster(t, cl).Spec.PodMetadata.Annotations[tlsNamesAnnotation]; got != namesHash(names) {
			t.Errorf("pod names annotation = %q, want %q to restart the pods", got, namesHash(names))
		}
	})
}
