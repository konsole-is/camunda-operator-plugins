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

package gatewayaccess_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	"sigs.k8s.io/yaml"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
	v1 "github.com/konsole-is/camunda-operator/api/v1"

	"github.com/konsole-is/camunda-operator-plugins/internal/gatewayaccess"
	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

// chartRegistrations returns the registrations of the values of the chart,
// as the controller loads them from the ConfigMap of the chart.
func chartRegistrations(dir string) ([]pluginsv1alpha1.PluginRegistration, error) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "charts", "gateway-access", "values.yaml"))
	if err != nil {
		return nil, err
	}

	var values struct {
		Registrations json.RawMessage `json:"registrations"`
	}
	if err := yaml.Unmarshal(raw, &values); err != nil {
		return nil, err
	}

	path := filepath.Join(dir, "registrations.yaml")
	if err := os.WriteFile(path, values.Registrations, 0o600); err != nil {
		return nil, err
	}
	return pluginkit.LoadRegistrations(path)
}

// accessSection is the core section access, as the operator writes it.
func accessSection() *pluginsv1alpha1.PluginSection {
	section := &pluginsv1alpha1.PluginSection{
		ObjectMeta: metav1.ObjectMeta{Name: "access"},
		Spec: pluginsv1alpha1.PluginSectionSpec{
			Label:            "Access",
			Order:            60,
			Selection:        pluginsv1alpha1.SelectionOne,
			EmptyOptionLabel: "No access from outside",
			Slot:             pluginsv1alpha1.SlotExternalAccess,
			AppliesTo: []pluginsv1alpha1.ClaimKind{
				pluginsv1alpha1.ClaimKindCamundaClusterClaim,
				pluginsv1alpha1.ClaimKindCamundaManagementClusterClaim,
			},
		},
	}
	for _, key := range []string{
		gatewayaccess.OutputExternalURL,
		gatewayaccess.OutputGRPCHost,
		gatewayaccess.OutputOptimizeURL,
		gatewayaccess.OutputManagementHost,
	} {
		section.Spec.Outputs = append(section.Spec.Outputs, pluginsv1alpha1.OutputDecl{Key: key})
	}
	return section
}

// installRBAC creates the ServiceAccount sa and the ClusterRoles of
// charts/gateway-access/templates/rbac.yaml for registrations, bound to sa.
// The conformance suite reads them to find the kinds of the plugin.
func installRBAC(
	ctx context.Context,
	c client.Client,
	sa pluginsv1alpha1.ServiceAccountRef,
	registrations []string,
) error {
	plugins := pluginsv1alpha1.GroupVersion.Group
	gateway := gatewayv1.GroupName
	roles := map[string][]rbacv1.PolicyRule{
		"gateway-access-serve": {
			{APIGroups: []string{plugins}, Resources: []string{"pluginregistrations"}, ResourceNames: registrations,
				Verbs: []string{"serve"}},
		},
		"gateway-access-controller": {
			{APIGroups: []string{plugins}, Resources: []string{"pluginregistrations"}, ResourceNames: registrations,
				Verbs: []string{"get", "create", "patch"}},
			{APIGroups: []string{plugins}, Resources: []string{"pluginregistrations/status"},
				ResourceNames: registrations, Verbs: []string{"update"}},
			{APIGroups: []string{plugins}, Resources: []string{"pluginrequests"},
				Verbs: []string{"get", "list", "watch", "update", "patch"}},
			{APIGroups: []string{plugins}, Resources: []string{"pluginrequests/status", "pluginrequests/finalizers"},
				Verbs: []string{"update"}},
			{APIGroups: []string{plugins}, Resources: []string{"grants"},
				Verbs: []string{"get", "list", "watch", "create", "update", "patch", "delete"}},
			{APIGroups: []string{"events.k8s.io"}, Resources: []string{"events"}, Verbs: []string{"create", "patch"}},
			{APIGroups: []string{gateway}, Resources: []string{"httproutes", "grpcroutes"},
				Verbs: []string{"get", "list", "watch", "create", "update", "patch", "delete"}},
			{APIGroups: []string{v1.GroupVersion.Group},
				Resources: []string{"camundaclusters", "camundaoptimizes", "camundamanagementclusters"},
				Verbs:     []string{"get", "list", "watch"}},
			{APIGroups: []string{gateway}, Resources: []string{"gateways", "gatewayclasses"},
				Verbs: []string{"get", "list", "watch"}},
		},
	}

	if err := c.Create(ctx, &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Namespace: sa.Namespace, Name: sa.Name},
	}); err != nil {
		return err
	}

	for name, rules := range roles {
		if err := c.Create(
			ctx,
			&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: name}, Rules: rules},
		); err != nil {
			return err
		}
		if err := c.Create(ctx, &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: name},
			Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Namespace: sa.Namespace, Name: sa.Name}},
		}); err != nil {
			return err
		}
	}
	return nil
}

// exampleClass is the GatewayClass example of the fake Gateway.
func exampleClass() *gatewayv1.GatewayClass {
	return &gatewayv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{Name: "example"},
		Spec:       gatewayv1.GatewayClassSpec{ControllerName: fakeGatewayController},
	}
}

// publicGateway is the Gateway gateway-system/public, with an HTTP listener
// and an HTTPS listener whose certificate is the Secret public-tls.
func publicGateway() *gatewayv1.Gateway {
	return &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Namespace: "gateway-system", Name: "public"},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: "example",
			Listeners: []gatewayv1.Listener{
				{Name: "http", Port: 80, Protocol: gatewayv1.HTTPProtocolType},
				{
					Name:     "https",
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

// certificateSecret is the Secret gateway-system/public-tls with a
// certificate that expires at notAfter.
func certificateSecret(notAfter time.Time) (*corev1.Secret, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "*.example.com"},
		NotBefore:    notAfter.Add(-90 * 24 * time.Hour),
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}

	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "gateway-system", Name: "public-tls"},
		Data: map[string][]byte{
			corev1.TLSCertKey: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		},
	}, nil
}

func httpEndpoint(name, service string, port int32, path string) v1.Endpoint {
	return v1.Endpoint{Name: name, Service: service, Port: port, PathPrefix: path, Protocol: v1.EndpointProtocolHTTP}
}

// standaloneEndpoints are the endpoints of the cluster name whose apps run
// beside the brokers.
func standaloneEndpoints(name string) []v1.Endpoint {
	return []v1.Endpoint{
		httpEndpoint("admin", name+"-admin", 8080, "/admin"),
		httpEndpoint("connectors", name+"-connectors", 8080, "/inbound"),
		httpEndpoint("default", name+"-zeebe-gateway", 8080, "/"),
		{Name: "grpc", Service: name + "-zeebe-gateway", Port: 26500, Protocol: v1.EndpointProtocolGRPC},
		httpEndpoint("operate", name+"-operate", 8080, "/operate"),
		httpEndpoint("rest", name+"-zeebe-gateway", 8080, "/v2"),
		httpEndpoint("tasklist", name+"-tasklist", 8080, "/tasklist"),
	}
}

// planeEndpoints are the endpoints of the management plane name.
func planeEndpoints(name string) []v1.Endpoint {
	return []v1.Endpoint{
		httpEndpoint("console", name+"-console", 8080, "/console"),
		httpEndpoint("identity", name+"-identity", 8084, "/identity"),
		httpEndpoint("keycloak", name+"-keycloak", 8080, "/auth"),
		httpEndpoint("web-modeler", name+"-web-modeler-webapp", 8070, "/modeler"),
		httpEndpoint("web-modeler-websockets", name+"-web-modeler-websockets", 8060, "/modeler-ws"),
	}
}

// newCluster returns a CamundaCluster with the smallest spec that its CRD
// admits.
func newCluster(namespace, name string) *v1.CamundaCluster {
	return &v1.CamundaCluster{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec:       v1.CamundaClusterSpec{PlatformConfigRef: "platform", StorageRef: "storage"},
	}
}

// newOptimize returns a CamundaOptimize of the cluster with the smallest spec
// that its CRD admits.
func newOptimize(namespace, name, cluster string) *v1.CamundaOptimize {
	return &v1.CamundaOptimize{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: v1.CamundaOptimizeSpec{
			Version:           "8.9.0",
			ManagementAuthRef: "auth",
			ClusterRef:        v1.ClusterRef{Name: cluster},
		},
	}
}

// newPlane returns a CamundaManagementCluster with the smallest spec that its
// CRD admits.
func newPlane(namespace, name string) *v1.CamundaManagementCluster {
	return &v1.CamundaManagementCluster{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: v1.CamundaManagementClusterSpec{
			PlatformConfigRef: "platform",
			IdentityProvider:  v1.IdentityProviderSpec{OIDC: &v1.ManagementOIDCSpec{}},
			Identity: v1.IdentitySpec{
				Version:           "8.9.0",
				ExternalURL:       "https://identity.example.com",
				DatabaseConfigRef: "identity-db",
				Admin:             v1.IdentityAdminSpec{ClaimName: "oid", ClaimValue: "admin"},
			},
		},
	}
}

// publishEndpoints writes endpoints into status.endpoints of obj. No
// controller reconciles the targets in these suites, so the write needs no
// proof that a controller read it.
func publishEndpoints(ctx context.Context, c client.Client, obj client.Object, endpoints []v1.Endpoint) error {
	switch o := obj.(type) {
	case *v1.CamundaCluster:
		o.Status.Endpoints = endpoints
	case *v1.CamundaOptimize:
		o.Status.Endpoints = endpoints
	case *v1.CamundaManagementCluster:
		o.Status.Endpoints = endpoints
	}
	return c.Status().Update(ctx, obj)
}

// gatewayParams are the parameters of a request on the HTTPS listener of the
// public Gateway.
func gatewayParams() gatewayaccess.Params {
	return gatewayaccess.Params{
		GatewayRef:                gatewayaccess.GatewayRef{Namespace: "gateway-system", Name: "public"},
		ListenerName:              "https",
		HostnamePattern:           "{name}.{namespace}.example.com",
		GRPCHostnamePattern:       "grpc-{name}.{namespace}.example.com",
		OptimizeHostnamePattern:   "optimize-{name}.{namespace}.example.com",
		ManagementHostnamePattern: "management-{name}.{namespace}.example.com",
	}
}

func rawParams(p gatewayaccess.Params) *apiextensionsv1.JSON {
	// A struct of strings always encodes.
	raw, _ := json.Marshal(p)
	return &apiextensionsv1.JSON{Raw: raw}
}

// accessRequest returns a request of the registration gateway-public, as a
// claim controller writes it, for the claim of the same name as the target.
func accessRequest(
	namespace, name string,
	target pluginsv1alpha1.TargetRef,
	p gatewayaccess.Params,
) *pluginsv1alpha1.PluginRequest {
	const registration = "gateway-public"
	claimKind := pluginsv1alpha1.ClaimKindCamundaClusterClaim
	if target.Kind == "CamundaManagementCluster" {
		claimKind = pluginsv1alpha1.ClaimKindCamundaManagementClusterClaim
	}

	return &pluginsv1alpha1.PluginRequest{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
			Labels: map[string]string{
				pluginsv1alpha1.LabelSection:      "access",
				pluginsv1alpha1.LabelRegistration: registration,
			},
		},
		Spec: pluginsv1alpha1.PluginRequestSpec{
			Section:        "access",
			Registration:   registration,
			ClaimRef:       pluginsv1alpha1.ClaimRef{Kind: claimKind, Name: target.Name},
			Target:         target,
			Parameters:     rawParams(p),
			DeletionPolicy: pluginsv1alpha1.DeletionPolicyDelete,
		},
	}
}
