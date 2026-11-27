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
	"slices"
	"testing"

	valkeyv1alpha1 "github.com/valkey-io/valkey-operator/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// setTestClusterDomain sets the cluster domain for one test. Tests that call
// it must not run in parallel.
func setTestClusterDomain(t *testing.T, domain string) {
	t.Helper()
	previous := clusterDomain
	t.Cleanup(func() { clusterDomain = previous })
	if err := SetClusterDomain(domain); err != nil {
		t.Fatal(err)
	}
}

func TestSetClusterDomain(t *testing.T) {
	tests := []struct {
		domain  string
		want    string
		wantErr bool
	}{
		{domain: "", want: "cluster.local"},
		{domain: "corp.example", want: "corp.example"},
		{domain: "corp.example.", want: "corp.example"},
		{domain: "Corp.Example", wantErr: true},
		{domain: "corp..example", wantErr: true},
		{domain: "corp_example", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.domain, func(t *testing.T) {
			previous := clusterDomain
			t.Cleanup(func() { clusterDomain = previous })
			clusterDomain = "unchanged"

			err := SetClusterDomain(tt.domain)
			if tt.wantErr {
				if err == nil || clusterDomain != "unchanged" {
					t.Fatalf("SetClusterDomain(%q) = %v, domain %q; want an error and no change", tt.domain, err, clusterDomain)
				}
				return
			}
			if err != nil || clusterDomain != tt.want {
				t.Fatalf("SetClusterDomain(%q) = %v, domain %q; want %q", tt.domain, err, clusterDomain, tt.want)
			}
		})
	}
}

func TestDefaultClusterDomainKeepsPlainSpec(t *testing.T) {
	setTestClusterDomain(t, "")
	c, _ := newTestContext(t, newTestInstance(`{"tls":{"mode":"disabled"}}`, false), interceptor.Funcs{})
	spec, err := buildClusterSpec(c)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Networking != nil {
		t.Errorf("networking = %+v, want none: releases before the setting applied none", spec.Networking)
	}

	c, _ = newTestContext(t, newTestInstance("", false), interceptor.Funcs{})
	spec, err = buildClusterSpec(c)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Networking == nil || spec.Networking.ClusterDomain != "cluster.local" {
		t.Errorf("networking = %+v, want clusterDomain cluster.local", spec.Networking)
	}
}

func TestCustomClusterDomain(t *testing.T) {
	setTestClusterDomain(t, "corp.example")
	headless := "valkey-cache.team-a.svc.corp.example"
	primary := "valkey-cache-primary.team-a.svc.corp.example"

	t.Run("TLS", func(t *testing.T) {
		c, cl := newTestContext(t, newTestInstance("", false), interceptor.Funcs{})
		if err := New(Options{}).Sync(c); err != nil {
			t.Fatalf("Sync: %v", err)
		}

		vc := &valkeyv1alpha1.ValkeyCluster{}
		if err := c.Get(vc, testInstance); err != nil {
			t.Fatal(err)
		}
		if got := vc.Spec.Networking.ClusterDomain; got != "corp.example" {
			t.Errorf("networking.clusterDomain = %q, want corp.example", got)
		}

		secret := getSecret(t, cl, tlsSecretName(testInstance))
		certs, err := parseCertificates(secret.Data[tlsSecretKeyCert])
		if err != nil {
			t.Fatal(err)
		}
		if got := certs[0].Subject.CommonName; got != headless {
			t.Errorf("CN = %q, want %q", got, headless)
		}
		for _, name := range []string{headless, "*." + headless, primary} {
			if !slices.Contains(certs[0].DNSNames, name) {
				t.Errorf("SANs %v miss %q", certs[0].DNSNames, name)
			}
		}

		cd, err := buildConnectionDetails(c)
		if err != nil {
			t.Fatal(err)
		}
		if cd.Host != primary {
			t.Errorf("host = %q, want %q", cd.Host, primary)
		}
	})

	t.Run("TLS disabled", func(t *testing.T) {
		c, _ := newTestContext(t, newTestInstance(`{"tls":{"mode":"disabled"}}`, false), interceptor.Funcs{})
		if err := New(Options{}).Sync(c); err != nil {
			t.Fatalf("Sync: %v", err)
		}
		spec, err := buildClusterSpec(c)
		if err != nil {
			t.Fatal(err)
		}
		if spec.Networking == nil || spec.Networking.ClusterDomain != "corp.example" {
			t.Errorf("networking = %+v, want clusterDomain corp.example", spec.Networking)
		}
		cd, err := buildConnectionDetails(c)
		if err != nil {
			t.Fatal(err)
		}
		if cd.Host != primary {
			t.Errorf("host = %q, want %q", cd.Host, primary)
		}
	})
}
