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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
)

// httpsListener is the name of the HTTPS listener of the test Gateway.
const httpsListener = "https"

var testNow = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

// certificatePEM returns a self-signed certificate in PEM that expires at
// notAfter.
func certificatePEM(t *testing.T, notAfter time.Time) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "orders.team-a.example.com"},
		NotBefore:    notAfter.Add(-90 * 24 * time.Hour),
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func testGateway() *gatewayv1.Gateway {
	return &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Namespace: "gateway-system", Name: "public"},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: "example",
			Listeners: []gatewayv1.Listener{
				{Name: "http", Port: 80, Protocol: gatewayv1.HTTPProtocolType},
				{
					Name:     httpsListener,
					Port:     443,
					Protocol: gatewayv1.HTTPSProtocolType,
					TLS: &gatewayv1.ListenerTLSConfig{
						CertificateRefs: []gatewayv1.SecretObjectReference{{Name: "public-tls"}},
					},
				},
			},
		},
	}
}

func tlsSecret(data []byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "gateway-system", Name: "public-tls"},
		Data:       map[string][]byte{corev1.TLSCertKey: data},
	}
}

func fakeReader(t *testing.T, funcs *interceptor.Funcs, objs ...client.Object) client.Reader {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, gatewayv1.Install(scheme))

	b := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...)
	if funcs != nil {
		b = b.WithInterceptorFuncs(*funcs)
	}
	return b.Build()
}

func TestExpiryItem_WarnsUnderTwentyOneDays(t *testing.T) {
	soon := testNow.Add(20 * 24 * time.Hour)
	item := expiryItem(soon, testNow)
	assert.Equal(t, pluginsv1alpha1.SummaryItem{
		Label:    "Certificate expires",
		Value:    soon.Format(time.DateOnly),
		Severity: pluginsv1alpha1.SeverityWarning,
	}, item)

	assert.Equal(t, pluginsv1alpha1.SeverityInfo, expiryItem(testNow.Add(22*24*time.Hour), testNow).Severity)
	assert.Equal(t, pluginsv1alpha1.SeverityWarning, expiryItem(testNow.Add(-time.Hour), testNow).Severity)
}

func TestCertificateItem_ReadsTheListenerOfTheParameters(t *testing.T) {
	notAfter := testNow.Add(10 * 24 * time.Hour)
	reader := fakeReader(t, nil, testGateway(), tlsSecret(certificatePEM(t, notAfter)))
	p := validParams()
	p.ListenerName = httpsListener

	item := CertificateItem(context.Background(), reader, p, testNow)
	require.NotNil(t, item)
	assert.Equal(t, "Certificate expires", item.Label)
	assert.Equal(t, notAfter.Format(time.DateOnly), item.Value)
	assert.Equal(t, pluginsv1alpha1.SeverityWarning, item.Severity)
}

func TestCertificateItem_TakesTheFirstHTTPSListenerWithoutAName(t *testing.T) {
	notAfter := testNow.Add(60 * 24 * time.Hour)
	reader := fakeReader(t, nil, testGateway(), tlsSecret(certificatePEM(t, notAfter)))

	item := CertificateItem(context.Background(), reader, validParams(), testNow)
	require.NotNil(t, item)
	assert.Equal(t, notAfter.Format(time.DateOnly), item.Value)
	assert.Equal(t, pluginsv1alpha1.SeverityInfo, item.Severity)
}

func TestCertificateItem_UnknownWhenTheListenerHasNoCertificate(t *testing.T) {
	reader := fakeReader(t, nil, testGateway())
	p := validParams()
	p.ListenerName = "http"

	item := CertificateItem(context.Background(), reader, p, testNow)
	require.NotNil(t, item)
	assert.True(t, strings.HasPrefix(item.Value, "unknown: "), item.Value)
	assert.Equal(t, pluginsv1alpha1.SeverityWarning, item.Severity)
}

func TestCertificateItem_UnknownWhenTheSecretCannotBeRead(t *testing.T) {
	missing := fakeReader(t, nil, testGateway())
	item := CertificateItem(context.Background(), missing, validParams(), testNow)
	require.NotNil(t, item)
	assert.True(t, strings.HasPrefix(item.Value, "unknown: "), item.Value)
	assert.Equal(t, pluginsv1alpha1.SeverityWarning, item.Severity)

	forbidden := fakeReader(t, &interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*corev1.Secret); ok {
				return apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, key.Name, nil)
			}
			return c.Get(ctx, key, obj, opts...)
		},
	}, testGateway(), tlsSecret(certificatePEM(t, testNow.Add(time.Hour))))
	item = CertificateItem(context.Background(), forbidden, validParams(), testNow)
	require.NotNil(t, item)
	assert.True(t, strings.HasPrefix(item.Value, "unknown: "), item.Value)
	assert.Contains(t, item.Value, "forbidden")
	assert.Equal(t, pluginsv1alpha1.SeverityWarning, item.Severity)
}

func TestCertificateItem_UnknownWhenTheCertificateIsNotPEM(t *testing.T) {
	reader := fakeReader(t, nil, testGateway(), tlsSecret([]byte("not a certificate")))

	item := CertificateItem(context.Background(), reader, validParams(), testNow)
	require.NotNil(t, item)
	assert.True(t, strings.HasPrefix(item.Value, "unknown: "), item.Value)
	assert.Equal(t, pluginsv1alpha1.SeverityWarning, item.Severity)
}

func TestCertificateItem_NoneForTheHTTPScheme(t *testing.T) {
	p := validParams()
	p.Scheme = "http"

	assert.Nil(t, CertificateItem(context.Background(), fakeReader(t, nil), p, testNow))
}

func TestCertificateItem_ReadsTheSecretInTheNamespaceOfItsReference(t *testing.T) {
	gw := testGateway()
	gw.Spec.Listeners[1].TLS.CertificateRefs[0].Namespace = ptr.To(gatewayv1.Namespace("certs"))
	secret := tlsSecret(certificatePEM(t, testNow.Add(60*24*time.Hour)))
	secret.Namespace = "certs"
	reader := fakeReader(t, nil, gw, secret)

	item := CertificateItem(context.Background(), reader, validParams(), testNow)
	require.NotNil(t, item)
	assert.Equal(t, pluginsv1alpha1.SeverityInfo, item.Severity)
}

func TestCertificateItem_UnknownWhenTheCertificateIsNotASecret(t *testing.T) {
	gateway := testGateway()
	gateway.Spec.Listeners[1].TLS.CertificateRefs[0].Group = ptr.To(gatewayv1.Group("example.com"))
	gateway.Spec.Listeners[1].TLS.CertificateRefs[0].Kind = ptr.To(gatewayv1.Kind("Certificate"))
	// A Secret with the name of the reference must not give the expiry.
	reader := fakeReader(t, nil, gateway, tlsSecret(certificatePEM(t, testNow.Add(90*24*time.Hour))))

	item := CertificateItem(context.Background(), reader, validParams(), testNow)
	require.NotNil(t, item)
	assert.Equal(
		t,
		`unknown: the listener certificate public-tls is a Certificate of the group "example.com", not a Secret`,
		item.Value,
	)
	assert.Equal(t, pluginsv1alpha1.SeverityWarning, item.Severity)
}
