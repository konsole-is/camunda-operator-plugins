/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package gatewayaccess

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
)

const (
	certificateLabel = "Certificate expires"
	// certificateWarning is how long before its expiry a certificate is a
	// warning.
	certificateWarning = 21 * 24 * time.Hour
)

// CertificateItem returns the summary item of the expiry of the listener
// certificate of the Gateway of p, or nil for the scheme http. The listener
// is p.ListenerName, or without a name the first HTTPS listener with a
// certificate. The item has the severity Warning when the certificate expires
// within 21 days. When the certificate cannot be read, the value is
// "unknown: <cause>" with the severity Warning.
func CertificateItem(
	ctx context.Context,
	reader client.Reader,
	p Params,
	now time.Time,
) *pluginsv1alpha1.SummaryItem {
	if p.scheme() == "http" {
		return nil
	}

	notAfter, err := certificateExpiry(ctx, reader, p)
	if err != nil {
		return &pluginsv1alpha1.SummaryItem{
			Label:    certificateLabel,
			Value:    "unknown: " + err.Error(),
			Severity: pluginsv1alpha1.SeverityWarning,
		}
	}

	item := expiryItem(notAfter, now)
	return &item
}

func certificateExpiry(ctx context.Context, reader client.Reader, p Params) (time.Time, error) {
	var gateway gatewayv1.Gateway
	key := client.ObjectKey{Namespace: p.GatewayRef.Namespace, Name: p.GatewayRef.Name}
	if err := reader.Get(ctx, key, &gateway); err != nil {
		return time.Time{}, fmt.Errorf("reading the Gateway %s: %w", key, err)
	}

	ref, err := certificateRef(&gateway, p.ListenerName)
	if err != nil {
		return time.Time{}, err
	}

	var secret corev1.Secret
	key = client.ObjectKey{
		Namespace: string(ptr.Deref(ref.Namespace, gatewayv1.Namespace(gateway.Namespace))),
		Name:      string(ref.Name),
	}
	if err := reader.Get(ctx, key, &secret); err != nil {
		return time.Time{}, fmt.Errorf("reading the Secret %s: %w", key, err)
	}

	block, _ := pem.Decode(secret.Data[corev1.TLSCertKey])
	if block == nil || block.Type != "CERTIFICATE" {
		return time.Time{}, fmt.Errorf("the Secret %s holds no PEM certificate in %s", key, corev1.TLSCertKey)
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return time.Time{}, fmt.Errorf("parsing the certificate of the Secret %s: %w", key, err)
	}

	return cert.NotAfter, nil
}

// certificateRef returns the first certificate reference of the listener
// name of gateway, or of its first HTTPS listener with one when name is empty.
func certificateRef(gateway *gatewayv1.Gateway, name string) (gatewayv1.SecretObjectReference, error) {
	for _, l := range gateway.Spec.Listeners {
		hasRef := l.TLS != nil && len(l.TLS.CertificateRefs) > 0
		switch {
		case name != "" && string(l.Name) == name && hasRef:
			return l.TLS.CertificateRefs[0], nil
		case name != "" && string(l.Name) == name:
			return gatewayv1.SecretObjectReference{}, fmt.Errorf("the listener %s has no certificate", name)
		case name == "" && l.Protocol == gatewayv1.HTTPSProtocolType && hasRef:
			return l.TLS.CertificateRefs[0], nil
		}
	}

	if name != "" {
		return gatewayv1.SecretObjectReference{}, fmt.Errorf("the Gateway has no listener %s", name)
	}
	return gatewayv1.SecretObjectReference{}, errors.New("the Gateway has no HTTPS listener with a certificate")
}

func expiryItem(notAfter, now time.Time) pluginsv1alpha1.SummaryItem {
	severity := pluginsv1alpha1.SeverityInfo
	if notAfter.Sub(now) < certificateWarning {
		severity = pluginsv1alpha1.SeverityWarning
	}

	return pluginsv1alpha1.SummaryItem{
		Label:    certificateLabel,
		Value:    notAfter.UTC().Format(time.DateOnly),
		Severity: severity,
	}
}
