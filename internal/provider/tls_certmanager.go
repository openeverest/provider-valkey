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
	"fmt"
	"slices"
	"time"

	cmapi "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	cmmeta "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-valkey/definition"
)

const (
	// certManagerGroup is the API group of cert-manager's own issuers.
	certManagerGroup = "cert-manager.io"

	// issuedCertificateWait is how often Sync checks for the first issuance:
	// the provider does not watch Certificates, which a cluster without
	// cert-manager does not serve.
	issuedCertificateWait = 10 * time.Second
)

// issuerReference is the cert-manager reference of the issuer the TLS
// parameters name.
func issuerReference(issuer *definition.IssuerReference) cmmeta.IssuerReference {
	return cmmeta.IssuerReference{Name: issuer.Name, Kind: string(issuer.Kind), Group: issuer.Group}
}

func describeIssuer(ref cmmeta.IssuerReference) string {
	kind, group := ref.Kind, ref.Group
	if kind == "" {
		kind = string(definition.IssuerKindIssuer)
	}
	if group == "" {
		group = certManagerGroup
	}
	return fmt.Sprintf("the %s %s of %s", kind, ref.Name, group)
}

// ensureIssuedCertificates requests the server certificate and the default
// user's client certificate from the issuer, and returns a WaitError until
// the server certificate is issued. On a running instance, a change of
// additional names waits for maintenance approval: Valkey reads the server
// certificate when it starts, so the pods restart to present the new one.
func ensureIssuedCertificates(c *controller.Context, tls definition.TLSParameters, deployed bool) error {
	ref := issuerReference(tls.Issuer)
	name := tlsIssuedSecretName(c.Name())
	want := namesHash(tls.AdditionalNames)

	current, found, err := getCertificate(c, name)
	if err != nil {
		return err
	}
	dnsNames := uniqueNames(serverDNSNames(c.Name(), c.Namespace()), tls.AdditionalNames)
	applyServer := true
	if found && current.Annotations[tlsNamesAnnotation] != want && deployed {
		applyServer = c.RequestMaintenance("tls-names-"+want,
			"Reissue the server certificate for spec.parameters.tls.additionalNames and restart the Valkey pods to present it",
			controller.MaintenanceRollingRestart)
	}
	if applyServer {
		server := &cmapi.Certificate{
			ObjectMeta: c.ObjectMeta(name),
			Spec: cmapi.CertificateSpec{
				SecretName: name,
				CommonName: headlessServiceFQDN(c.Name(), c.Namespace()),
				DNSNames:   dnsNames,
				IssuerRef:  ref,
				Usages: []cmapi.KeyUsage{
					cmapi.UsageDigitalSignature, cmapi.UsageKeyEncipherment,
					cmapi.UsageServerAuth, cmapi.UsageClientAuth,
				},
			},
		}
		server.Annotations = map[string]string{tlsNamesAnnotation: want}
		if err := c.Apply(server); err != nil {
			if meta.IsNoMatchError(err) {
				return fmt.Errorf("spec.parameters.tls.issuer needs cert-manager, which the cluster does not serve: %w", err)
			}
			return fmt.Errorf("apply the server Certificate: %w", err)
		}
	}

	clientCert := &cmapi.Certificate{
		ObjectMeta: c.ObjectMeta(tlsClientSecretName(c.Name())),
		Spec: cmapi.CertificateSpec{
			SecretName: tlsClientSecretName(c.Name()),
			CommonName: defaultUsername,
			IssuerRef:  ref,
			Usages: []cmapi.KeyUsage{
				cmapi.UsageDigitalSignature, cmapi.UsageKeyEncipherment, cmapi.UsageClientAuth,
			},
		},
	}
	if err := c.Apply(clientCert); err != nil {
		return fmt.Errorf("apply the client Certificate: %w", err)
	}

	if !applyServer {
		dnsNames = current.Spec.DNSNames
	}
	// Sync waits for the certificate to cover the names: the provider does
	// not watch Secrets, and the pods restart to present a reissued one only
	// once it is there.
	secret := &corev1.Secret{}
	issued, err := getOptional(c, secret, name)
	if err != nil {
		return err
	}
	if !issued || !certificateCovers(secret.Data[tlsSecretKeyCert], dnsNames) {
		return controller.WaitForDuration(
			fmt.Sprintf("waiting for cert-manager to issue the server certificate from %s", describeIssuer(ref)),
			issuedCertificateWait)
	}
	return nil
}

// certificateCovers reports whether the leaf certificate of certPEM lists
// all names.
func certificateCovers(certPEM []byte, names []string) bool {
	chain, err := parseCertificates(certPEM)
	if err != nil {
		return false
	}
	for _, name := range names {
		if !slices.Contains(chain[0].DNSNames, name) {
			return false
		}
	}
	return true
}

// issuedNamesHash returns the hash of the additional names the issued server
// certificate covers: that of the Certificate once cert-manager has written
// a certificate with all its names, and otherwise the hash the pods already
// carry, so they restart only once the new certificate is there.
func issuedNamesHash(c *controller.Context, tls definition.TLSParameters, podHash string) (string, error) {
	certificate, found, err := getCertificate(c, tlsIssuedSecretName(c.Name()))
	if err != nil || !found {
		return podHash, err
	}
	want := certificate.Annotations[tlsNamesAnnotation]
	if want != namesHash(tls.AdditionalNames) {
		// The new names wait for approval: the Certificate still has the
		// ones the pods present.
		return want, nil
	}
	secret := &corev1.Secret{}
	found, err = getOptional(c, secret, tlsIssuedSecretName(c.Name()))
	if err != nil || !found {
		return podHash, err
	}
	if !certificateCovers(secret.Data[tlsSecretKeyCert], certificate.Spec.DNSNames) {
		return podHash, nil
	}
	return want, nil
}

// getCertificate reads a cert-manager Certificate from the API server: the
// client caches no Certificates, which a cluster without cert-manager does
// not even serve.
func getCertificate(c *controller.Context, name string) (*cmapi.Certificate, bool, error) {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(cmapi.SchemeGroupVersion.WithKind(cmapi.CertificateKind))
	if err := c.Get(obj, name); err != nil {
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("get the Certificate %s: %w", name, err)
	}
	certificate := &cmapi.Certificate{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, certificate); err != nil {
		return nil, false, fmt.Errorf("decode the Certificate %s: %w", name, err)
	}
	return certificate, true, nil
}

// deleteIssuedCertificates deletes the Certificates of an issuer chosen
// before the instance was created with another certificate source, so they
// do not overwrite its Secrets.
func deleteIssuedCertificates(c *controller.Context) error {
	for _, name := range []string{tlsIssuedSecretName(c.Name()), tlsClientSecretName(c.Name())} {
		if _, found, err := getCertificate(c, name); err != nil || !found {
			if err != nil {
				return err
			}
			continue
		}
		if err := c.Delete(&cmapi.Certificate{ObjectMeta: c.ObjectMeta(name)}); err != nil {
			return fmt.Errorf("delete the Certificate %s: %w", name, err)
		}
	}
	return nil
}

// uniqueNames joins the lists of names, each name once, in order.
func uniqueNames(lists ...[]string) []string {
	var names []string
	for _, name := range slices.Concat(lists...) {
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	return names
}
