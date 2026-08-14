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
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"testing"
)

func TestGenerateTLSMaterial(t *testing.T) {
	caPEM, certPEM, keyPEM, err := generateTLSMaterial("mycache", "team-a")
	if err != nil {
		t.Fatalf("generateTLSMaterial returned error: %v", err)
	}

	// The key pair must be valid and match.
	if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
		t.Fatalf("server cert/key do not form a valid pair: %v", err)
	}

	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caPEM) {
		t.Fatal("failed to parse generated CA certificate")
	}

	serverCert := parseCert(t, certPEM)

	// The server cert must be signed by the generated CA for the FQDN the
	// operator verifies against.
	wantFQDN := "valkey-mycache.team-a.svc.cluster.local"
	if _, err := serverCert.Verify(x509.VerifyOptions{
		DNSName: wantFQDN,
		Roots:   caPool,
	}); err != nil {
		t.Fatalf("server cert not verifiable for %q against generated CA: %v", wantFQDN, err)
	}

	assertSAN(t, serverCert, wantFQDN)
	assertSAN(t, serverCert, "*."+wantFQDN)
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

func assertSAN(t *testing.T, cert *x509.Certificate, name string) {
	t.Helper()
	for _, dns := range cert.DNSNames {
		if dns == name {
			return
		}
	}
	t.Fatalf("expected SAN %q in %v", name, cert.DNSNames)
}
