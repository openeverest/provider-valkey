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
	"maps"
	"testing"

	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	valkeyv1alpha1 "github.com/valkey-io/valkey-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/openeverest/provider-valkey/internal/common"
)

func valkeyNode(nodeIndex, role string) *valkeyv1alpha1.ValkeyNode {
	return &valkeyv1alpha1.ValkeyNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testInstance + "-0-" + nodeIndex,
			Namespace: testNamespace,
			Labels: map[string]string{
				clusterLabel:             testInstance,
				shardLabel:               "0",
				nodeIndexLabel:           nodeIndex,
				controller.ProviderLabel: common.ProviderName,
			},
		},
		Status: valkeyv1alpha1.ValkeyNodeStatus{Role: role},
	}
}

func getService(t *testing.T, cl client.Client, name string) (*corev1.Service, bool) {
	t.Helper()
	svc := &corev1.Service{}
	err := cl.Get(context.Background(), client.ObjectKey{Namespace: testNamespace, Name: name}, svc)
	if apierrors.IsNotFound(err) {
		return nil, false
	}
	if err != nil {
		t.Fatalf("getting service %q: %v", name, err)
	}
	return svc, true
}

func TestPrimaryServiceFollowsPrimary(t *testing.T) {
	tests := []struct {
		name          string
		nodes         []client.Object
		wantNodeIndex string
	}{
		{name: "before any node reports a role", wantNodeIndex: "0"},
		{name: "initial primary", nodes: []client.Object{valkeyNode("0", "primary"), valkeyNode("1", "replica")}, wantNodeIndex: "0"},
		{name: "after a failover", nodes: []client.Object{valkeyNode("0", "replica"), valkeyNode("1", "primary")}, wantNodeIndex: "1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, cl := newTestContext(t, newTestInstance("", false), interceptor.Funcs{}, tt.nodes...)
			if err := New(ScrapeConfig{}).Sync(c); err != nil {
				t.Fatalf("Sync: %v", err)
			}
			svc, ok := getService(t, cl, primaryServiceName(testInstance))
			if !ok {
				t.Fatal("primary Service not created")
			}
			want := map[string]string{clusterLabel: testInstance, shardLabel: "0", nodeIndexLabel: tt.wantNodeIndex}
			if !maps.Equal(svc.Spec.Selector, want) {
				t.Fatalf("selector = %v, want %v", svc.Spec.Selector, want)
			}
			if len(svc.Spec.Ports) != 1 || svc.Spec.Ports[0].Port != valkeyPortNumber {
				t.Fatalf("ports = %+v", svc.Spec.Ports)
			}
		})
	}
}

func TestClusterTopologyHasNoPrimaryService(t *testing.T) {
	in := newTestInstance("", false)
	in.Spec.Topology.Type = common.TopologyCluster
	c, cl := newTestContext(t, in, interceptor.Funcs{})
	if err := New(ScrapeConfig{}).Sync(c); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if _, ok := getService(t, cl, primaryServiceName(testInstance)); ok {
		t.Fatal("cluster topology must not get a primary Service")
	}
	host, err := connectionHost(c)
	if err != nil {
		t.Fatal(err)
	}
	if want := "valkey-cache.team-a.svc.cluster.local"; host != want {
		t.Fatalf("host = %q, want %q", host, want)
	}
}

func TestReplicationConnectionHost(t *testing.T) {
	primary := "valkey-cache-primary.team-a.svc.cluster.local"
	headless := "valkey-cache.team-a.svc.cluster.local"

	t.Run("TLS disabled", func(t *testing.T) {
		c, _ := newTestContext(t, newTestInstance(`{"tls":{"mode":"disabled"}}`, false), interceptor.Funcs{})
		if err := New(ScrapeConfig{}).Sync(c); err != nil {
			t.Fatalf("Sync: %v", err)
		}
		cd, err := buildConnectionDetails(c)
		if err != nil {
			t.Fatal(err)
		}
		if cd.Host != primary {
			t.Fatalf("host = %q, want %q", cd.Host, primary)
		}
	})

	t.Run("TLS certificate covers the primary Service", func(t *testing.T) {
		c, _ := newTestContext(t, newTestInstance("", false), interceptor.Funcs{})
		if err := New(ScrapeConfig{}).Sync(c); err != nil {
			t.Fatalf("Sync: %v", err)
		}
		cd, err := buildConnectionDetails(c)
		if err != nil {
			t.Fatal(err)
		}
		if cd.Host != primary {
			t.Fatalf("host = %q, want %q", cd.Host, primary)
		}
	})

	t.Run("TLS certificate issued before the primary Service", func(t *testing.T) {
		ca, err := newCertAuthority(testInstance)
		if err != nil {
			t.Fatal(err)
		}
		tmpl := serverCertTemplate(testInstance, testNamespace)
		tmpl.DNSNames = []string{headless, "*." + headless, "localhost"}
		certPEM, keyPEM, err := ca.issue(tmpl)
		if err != nil {
			t.Fatal(err)
		}
		legacy := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: tlsSecretName(testInstance), Namespace: testNamespace},
			Data:       map[string][]byte{tlsSecretKeyCA: ca.certPEM, tlsSecretKeyCert: certPEM, tlsSecretKeyKey: keyPEM},
		}
		c, _ := newTestContext(t, newTestInstance("", false), interceptor.Funcs{}, legacy)
		host, err := connectionHost(c)
		if err != nil {
			t.Fatal(err)
		}
		if host != headless {
			t.Fatalf("host = %q, want %q", host, headless)
		}
	})
}

func TestInstanceForValkeyNode(t *testing.T) {
	got := instanceForValkeyNode(context.Background(), valkeyNode("1", "primary"))
	if len(got) != 1 || got[0].Name != testInstance || got[0].Namespace != testNamespace {
		t.Fatalf("requests = %v", got)
	}
	if got := instanceForValkeyNode(context.Background(), &valkeyv1alpha1.ValkeyNode{}); got != nil {
		t.Fatalf("unlabelled node mapped to %v", got)
	}
}
