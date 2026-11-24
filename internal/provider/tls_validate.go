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
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	valkeyv1alpha1 "github.com/valkey-io/valkey-operator/api/v1alpha1"

	"github.com/openeverest/provider-valkey/definition"
	"github.com/openeverest/provider-valkey/internal/common"
)

// The values the TLS parameters accept.
var (
	tlsModes    = []definition.TLSMode{definition.TLSModeRequire, definition.TLSModeDisabled}
	issuerKinds = []definition.IssuerKind{definition.IssuerKindIssuer, definition.IssuerKindClusterIssuer}
)

// ownSecretSuffixes name, after the Instance, the Secrets the provider, the
// runtime and cert-manager write for it.
var ownSecretSuffixes = []string{
	tlsSecretSuffix, tlsCASecretSuffix, tlsClientSecretSuffix, tlsIssuedSecretSuffix, "-auth", "-conn",
}

const (
	needsTLSDetail = "needs transport encryption (tls.mode=requireTLS)"

	tlsModeForbidden = "cannot change on an existing instance: valkey-operator cannot switch the " +
		"nodes' transport without breaking the links between them; set the mode when the instance is created"
	tlsSourceForbidden = "certificateSecretName and issuer cannot change on an existing instance: "

	// tlsAutoReloadConfig has Valkey, from 9.1, reload its certificate files
	// when they change, which a renewal does.
	tlsAutoReloadConfig = "tls-auto-reload-interval"

	// tlsAutoReloadInterval is how often, in seconds, Valkey checks its
	// certificate files by default. cert-manager renews a certificate well
	// before it expires (a third of its lifetime by default).
	tlsAutoReloadInterval = "3600"

	// tlsAutoReloadMinVersion is the first Valkey version with
	// tlsAutoReloadConfig.
	tlsAutoReloadMinVersion = "9.1"
)

// validateTLS checks the TLS parameters of in on their own. It warns that
// Valkey versions without tlsAutoReloadConfig keep serving a certificate
// cert-manager or the user renews until they restart.
func validateTLS(spec *corev1alpha1.ProviderSpec, in *corev1alpha1.Instance) (field.ErrorList, []string) {
	cfg, err := instanceTLS(in)
	if err != nil {
		return field.ErrorList{field.Invalid(field.NewPath("spec", "parameters"), field.OmitValueType{}, err.Error())}, nil
	}

	var errs field.ErrorList
	if !slices.Contains(tlsModes, cfg.Mode) {
		errs = append(errs, field.NotSupported(tlsPath.Child("mode"), cfg.Mode, tlsModes))
	}
	if cfg.Mode == definition.TLSModeDisabled {
		if cfg.CertificateSecretName != "" || cfg.Issuer != nil || len(cfg.AdditionalNames) > 0 {
			errs = append(errs, field.Forbidden(tlsPath,
				"certificateSecretName, issuer and additionalNames apply only with TLS on: set mode to requireTLS"))
		}
		if cfg.ClientAuth == definition.TLSClientAuthRequired {
			errs = append(errs, field.Invalid(tlsPath.Child("clientAuth"), cfg.ClientAuth, needsTLSDetail))
		}
		if cfg.CertificateUser == definition.TLSCertificateUserCN {
			errs = append(errs, field.Invalid(tlsPath.Child("certificateUser"), cfg.CertificateUser, needsTLSDetail))
		}
		return errs, nil
	}

	errs = append(errs, validateCertificateSource(in.Name, cfg)...)
	if cfg.CertificateUser == definition.TLSCertificateUserCN {
		if cfg.ClientAuth == definition.TLSClientAuthDisabled {
			errs = append(errs, field.Invalid(tlsPath.Child("certificateUser"), cfg.CertificateUser,
				"has no effect with tls.clientAuth=disabled"))
		} else {
			errs = append(errs, validateCertificateUserVersion(spec, in)...)
		}
	}

	var warnings []string
	if certificateSourceOf(cfg) != tlsSourceManaged {
		if version, known := engineVersion(spec, in); known && !reloadsCertificates(version) &&
			!engineConfigSets(in, tlsAutoReloadConfig) {
			warnings = append(warnings, fmt.Sprintf(
				"Valkey %s reads its certificate only when it starts: restart the Valkey pods after the "+
					"certificate is renewed, or run Valkey %s or later, which reloads it", version, tlsAutoReloadMinVersion))
		}
	}
	return errs, warnings
}

// validateCertificateSource checks certificateSecretName, issuer and
// additionalNames.
func validateCertificateSource(instance string, cfg definition.TLSParameters) field.ErrorList {
	var errs field.ErrorList
	if name := cfg.CertificateSecretName; name != "" {
		path := tlsPath.Child("certificateSecretName")
		if msgs := validation.IsDNS1123Subdomain(name); len(msgs) > 0 {
			errs = append(errs, field.Invalid(path, name, strings.Join(msgs, "; ")))
		}
		for _, suffix := range ownSecretSuffixes {
			if name == instance+suffix {
				errs = append(errs, field.Invalid(path, name,
					"the name of a Secret created for the instance, which would overwrite yours; name the Secret differently"))
			}
		}
	}

	if issuer := cfg.Issuer; issuer != nil {
		path := tlsPath.Child("issuer")
		if cfg.CertificateSecretName != "" {
			errs = append(errs, field.Forbidden(path, "use either certificateSecretName or an issuer, not both"))
		}
		if issuer.Name == "" {
			errs = append(errs, field.Required(path.Child("name"), ""))
		} else if msgs := validation.IsDNS1123Subdomain(issuer.Name); len(msgs) > 0 {
			errs = append(errs, field.Invalid(path.Child("name"), issuer.Name, strings.Join(msgs, "; ")))
		}
		if issuer.Group == certManagerGroup && !slices.Contains(issuerKinds, issuer.Kind) {
			errs = append(errs, field.NotSupported(path.Child("kind"), issuer.Kind, issuerKinds))
		}
	}

	namesPath := tlsPath.Child("additionalNames")
	if len(cfg.AdditionalNames) > 0 && cfg.CertificateSecretName != "" {
		errs = append(errs, field.Forbidden(namesPath,
			"cannot be combined with certificateSecretName: the certificate in the Secret carries its own names"))
	}
	for i, name := range cfg.AdditionalNames {
		if net.ParseIP(name) != nil {
			errs = append(errs, field.Invalid(namesPath.Index(i), name, "must be a DNS name, not an IP address"))
			continue
		}
		if msgs := validation.IsDNS1123Subdomain(strings.TrimPrefix(name, "*.")); len(msgs) > 0 {
			errs = append(errs, field.Invalid(namesPath.Index(i), name, "must be a DNS name: "+strings.Join(msgs, "; ")))
		}
	}
	return errs
}

// certificateSource describes where an instance's certificate comes from.
func certificateSource(cfg definition.TLSParameters) string {
	switch certificateSourceOf(cfg) {
	case tlsSourceSecret:
		return "the Secret " + cfg.CertificateSecretName
	case tlsSourceIssuer:
		return describeIssuer(issuerReference(cfg.Issuer))
	}
	return "the CA created for the instance"
}

// validateTLSTransition rejects, on an update of an instance the provider has
// created, changing the TLS mode or the certificate source, and moving off
// required client certificates. validateTLSReferences checks the same at
// reconcile time, against the ValkeyCluster.
//
// valkey-operator (as of v0.9.0) switches the nodes' transport in one step,
// which breaks the cluster bus and replication links between nodes that
// rolled and nodes that did not. It also stops presenting its own client
// certificate as soon as the spec no longer requires one, which locks it out
// of the nodes that have not rolled yet.
//
// The provider has created the ValkeyCluster once it has reconciled the
// Instance, which its observed generation records. The phase can't tell: it
// stays Pending until the cluster is first available. Left to
// validateTLSReferences are an Instance the runtime rejects, which may never
// have had a cluster, and one of a release that kept TLS in the engine
// parameters, where this release does not read them.
func validateTLSTransition(in, old *corev1alpha1.Instance) field.ErrorList {
	if old == nil || old.Status.ObservedGeneration < 1 || rejected(old) || hasEngineTLSParameters(old) {
		return nil
	}
	before, err := instanceTLS(old)
	if err != nil {
		return nil
	}
	after, err := instanceTLS(in)
	if err != nil {
		return nil
	}
	if before.Mode != after.Mode {
		return field.ErrorList{field.Forbidden(tlsPath.Child("mode"), tlsModeForbidden)}
	}
	if before.Mode == definition.TLSModeDisabled {
		return nil
	}
	var errs field.ErrorList
	if from, to := certificateSource(before), certificateSource(after); from != to {
		errs = append(errs, field.Forbidden(tlsPath, tlsSourceForbidden+"the certificate comes from "+from))
	}
	if before.ClientAuth == definition.TLSClientAuthRequired && after.ClientAuth != definition.TLSClientAuthRequired {
		errs = append(errs, field.Forbidden(tlsPath.Child("clientAuth"), clientAuthRelaxedDetail))
	}
	return errs
}

// rejected reports whether the runtime rejects in as invalid.
func rejected(in *corev1alpha1.Instance) bool {
	ready := apimeta.FindStatusCondition(in.Status.Conditions, corev1alpha1.ConditionReady)
	return ready != nil && ready.Status == metav1.ConditionFalse && ready.Reason == corev1alpha1.ReasonValidationFailed
}

// hasEngineTLSParameters reports whether in carries TLS in the engine
// parameters, where releases before spec.parameters.tls kept it.
func hasEngineTLSParameters(in *corev1alpha1.Instance) bool {
	params := in.Spec.Components[common.ComponentEngine].Parameters
	if params == nil || len(params.Raw) == 0 {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(params.Raw, &fields) != nil {
		return false
	}
	_, ok := fields["tls"]
	return ok
}

// validateTLSReferences is validateTLSTransition against the running
// ValkeyCluster, and the Certificate of an issuer: the previous Instance is
// not always at hand. It also checks the certificate of
// certificateSecretName, once the Secret exists (Sync waits for a missing
// one), and that an instance of the provider's CA has the CA key to issue
// the certificates its settings need.
func validateTLSReferences(c *controller.Context) (field.ErrorList, error) {
	cfg := tlsParameters(c.Instance())
	var errs field.ErrorList

	if cfg.Mode != definition.TLSModeDisabled && cfg.CertificateSecretName != "" {
		secret := &corev1.Secret{}
		found, err := getOptional(c, secret, cfg.CertificateSecretName)
		if err != nil {
			return nil, err
		}
		if found {
			names := internalTLSNames(c)
			exporter, err := exporterRequired(c)
			if err != nil {
				return nil, err
			}
			if exporter {
				// redis_exporter (as of v1.80.0) ignores the server name
				// valkey-operator sets and verifies the certificate for
				// localhost, the host it connects to.
				names = append(names, "localhost")
			}
			if err := checkCertificateSecret(secret, names); err != nil {
				errs = append(errs, field.Invalid(tlsPath.Child("certificateSecretName"), secret.Name, err.Error()))
			}
		}
	}

	vc := &valkeyv1alpha1.ValkeyCluster{}
	deployed, err := getOptional(c, vc, c.Name())
	if err != nil {
		return nil, fmt.Errorf("get ValkeyCluster: %w", err)
	}
	if deployed {
		deployedErrs, err := validateDeployedTLS(c, cfg, vc)
		if err != nil {
			return nil, err
		}
		errs = append(errs, deployedErrs...)
	}
	if len(errs) > 0 || cfg.Mode == definition.TLSModeDisabled || certificateSourceOf(cfg) != tlsSourceManaged {
		return errs, nil
	}
	return validateCAKey(c, cfg)
}

// validateDeployedTLS checks the TLS parameters against those vc was applied
// with.
func validateDeployedTLS(c *controller.Context, cfg definition.TLSParameters, vc *valkeyv1alpha1.ValkeyCluster) (field.ErrorList, error) {
	applied := vc.GetTLS()
	if (applied == nil) != (cfg.Mode == definition.TLSModeDisabled) {
		return field.ErrorList{field.Forbidden(tlsPath.Child("mode"), tlsModeForbidden)}, nil
	}
	if applied == nil {
		return nil, nil
	}

	var errs field.ErrorList
	if applied.ClientAuthMode() == valkeyv1alpha1.TLSAuthClientsRequired && cfg.ClientAuth != definition.TLSClientAuthRequired {
		errs = append(errs, field.Forbidden(tlsPath.Child("clientAuth"), clientAuthRelaxedDetail))
	}

	secretName := applied.Certificates.Server.SecretName
	if secretName != serverSecretName(c.Name(), cfg) {
		from := "the Secret " + secretName
		switch secretName {
		case tlsSecretName(c.Name()):
			from = "the CA created for the instance"
		case tlsIssuedSecretName(c.Name()):
			from = "cert-manager"
		}
		return append(errs, field.Forbidden(tlsPath, tlsSourceForbidden+"the instance serves the certificate of "+from)), nil
	}
	if certificateSourceOf(cfg) != tlsSourceIssuer {
		return errs, nil
	}

	certificate, found, err := getCertificate(c, tlsIssuedSecretName(c.Name()))
	if err != nil || !found {
		return errs, err
	}
	current, want := certificate.Spec.IssuerRef, issuerReference(cfg.Issuer)
	if current.Name != want.Name || defaultString(current.Kind, string(definition.IssuerKindIssuer)) != want.Kind ||
		defaultString(current.Group, certManagerGroup) != want.Group {
		errs = append(errs, field.Forbidden(tlsPath.Child("issuer"),
			tlsSourceForbidden+"the server certificate is issued by "+describeIssuer(current)))
	}
	return errs, nil
}

// validateCAKey reports an instance of the provider's CA whose settings need
// a certificate it cannot issue. Instances created before the provider kept
// the CA key cannot have a client certificate, nor a server certificate for
// additional names, issued without replacing their CA.
func validateCAKey(c *controller.Context, cfg definition.TLSParameters) (field.ErrorList, error) {
	server := &corev1.Secret{}
	hasServerCert, err := getOptional(c, server, tlsSecretName(c.Name()))
	if err != nil || !hasServerCert {
		return nil, err
	}
	hasCA, err := c.Exists(&corev1.Secret{}, tlsCASecretName(c.Name()))
	if err != nil || hasCA {
		return nil, err
	}

	var path *field.Path
	var value any
	switch {
	case managedNamesHash(server) != namesHash(cfg.AdditionalNames):
		path, value = tlsPath.Child("additionalNames"), cfg.AdditionalNames
	case clientCertificateNeeded(cfg):
		hasClientCert, err := c.Exists(&corev1.Secret{}, tlsClientSecretName(c.Name()))
		if err != nil || hasClientCert {
			return nil, err
		}
		path, value = tlsPath.Child("certificateUser"), cfg.CertificateUser
		if cfg.ClientAuth == definition.TLSClientAuthRequired {
			path, value = tlsPath.Child("clientAuth"), cfg.ClientAuth
		}
	default:
		return nil, nil
	}
	return field.ErrorList{field.Invalid(path, value, fmt.Sprintf(
		"the instance has no CA key to issue certificates with; "+
			"delete the %q secret to regenerate the TLS material, then restart the Valkey pods",
		tlsSecretName(c.Name()),
	))}, nil
}

// internalTLSNames are the names the operator verifies the nodes'
// certificates against, and those of the pods, which the nodes announce to
// cluster clients. A certificate of certificateSecretName has to be valid
// for all of them.
func internalTLSNames(c *controller.Context) []string {
	fqdn := headlessServiceFQDN(c.Name(), c.Namespace())
	names := []string{fqdn}
	shards, nodes := resolveShards(c.Instance()), 1+replicasPerShard(c.Instance())
	for shard := range shards {
		for node := range nodes {
			names = append(names, fmt.Sprintf("%s%s-%d-%d-0.%s", headlessServicePrefix, c.Name(), shard, node, fqdn))
		}
	}
	return names
}

// checkCertificateSecret checks a certificate Secret the user provides: it
// holds a certificate and its key (tls.crt, tls.key) and the CA certificate
// (ca.crt), the CA verifies the certificate for both server and client
// authentication, as the nodes present it to each other on the cluster bus
// and for replication, and the certificate is valid for names.
func checkCertificateSecret(secret *corev1.Secret, names []string) error {
	var missing []string
	for _, key := range []string{tlsSecretKeyCert, tlsSecretKeyKey, tlsSecretKeyCA} {
		if len(secret.Data[key]) == 0 {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("the Secret has no %s", strings.Join(missing, ", "))
	}
	if _, err := tls.X509KeyPair(secret.Data[tlsSecretKeyCert], secret.Data[tlsSecretKeyKey]); err != nil {
		return fmt.Errorf("tls.crt and tls.key do not hold a certificate and its private key: %w", err)
	}
	chain, err := parseCertificates(secret.Data[tlsSecretKeyCert])
	if err != nil {
		return fmt.Errorf("tls.crt: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(secret.Data[tlsSecretKeyCA]) {
		return errors.New("ca.crt holds no PEM certificate")
	}
	intermediates := x509.NewCertPool()
	for _, cert := range chain[1:] {
		intermediates.AddCert(cert)
	}
	leaf := chain[0]
	for _, usage := range []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth} {
		if _, err := leaf.Verify(x509.VerifyOptions{
			Roots:         roots,
			Intermediates: intermediates,
			KeyUsages:     []x509.ExtKeyUsage{usage},
		}); err != nil {
			return fmt.Errorf("the CA of ca.crt does not verify the certificate of tls.crt for %s authentication, "+
				"which the Valkey nodes use it for: %w", extKeyUsageName(usage), err)
		}
	}
	var uncovered []string
	for _, name := range names {
		if leaf.VerifyHostname(name) != nil {
			uncovered = append(uncovered, name)
		}
	}
	if len(uncovered) > 0 {
		return fmt.Errorf("the certificate is not valid for %s: valkey-operator, the metrics exporter and cluster clients connect by these names",
			strings.Join(uncovered, ", "))
	}
	return nil
}

func extKeyUsageName(usage x509.ExtKeyUsage) string {
	if usage == x509.ExtKeyUsageClientAuth {
		return "client"
	}
	return "server"
}

// validateCertificateUserVersion rejects certificate-to-user mapping on Valkey
// versions without it. A custom image has no known version and is accepted.
func validateCertificateUserVersion(spec *corev1alpha1.ProviderSpec, in *corev1alpha1.Instance) field.ErrorList {
	version, known := engineVersion(spec, in)
	if !known {
		return nil
	}
	major, err := strconv.Atoi(strings.SplitN(version, ".", 2)[0])
	if err != nil || major >= certificateUserMinMajorVersion {
		return nil
	}
	return field.ErrorList{field.Invalid(tlsPath.Child("certificateUser"), definition.TLSCertificateUserCN,
		fmt.Sprintf("requires Valkey %d.0 or later, the instance runs %s", certificateUserMinMajorVersion, version),
	)}
}

// reloadsCertificates reports whether Valkey version, major.minor[.patch],
// has tlsAutoReloadConfig. An unparsable version is assumed to have it.
func reloadsCertificates(version string) bool {
	parts := strings.SplitN(version, ".", 3)
	if len(parts) < 2 {
		return true
	}
	major, errMajor := strconv.Atoi(parts[0])
	minor, errMinor := strconv.Atoi(parts[1])
	if errMajor != nil || errMinor != nil {
		return true
	}
	return major > 9 || major == 9 && minor >= 1
}

// engineConfigSets reports whether the engine's spec.config sets key.
func engineConfigSets(in *corev1alpha1.Instance, key string) bool {
	_, ok := engineConfig(in)[key]
	return ok
}

// engineVersion returns the Valkey version the instance runs, resolved the way
// the runtime does before Sync. known is false for a custom image.
func engineVersion(spec *corev1alpha1.ProviderSpec, in *corev1alpha1.Instance) (version string, known bool) {
	engine := in.Spec.Components[common.ComponentEngine]
	if engine.Image != "" {
		return "", false
	}
	if engine.Version != "" {
		return engine.Version, true
	}

	if name := controller.EffectiveVersionBundleName(spec, in); name != "" {
		if bundle, err := controller.ResolveVersionBundle(spec, name); err == nil && bundle.Components[common.ComponentEngine] != "" {
			return bundle.Components[common.ComponentEngine], true
		}
	}
	if ct, ok := spec.ComponentTypes[controller.GetComponentType(spec, common.ComponentEngine)]; ok && ct.DefaultVersion != "" {
		return ct.DefaultVersion, true
	}
	return "", false
}

func defaultString(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
