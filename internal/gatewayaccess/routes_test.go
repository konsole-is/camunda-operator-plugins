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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	v1 "github.com/konsole-is/camunda-operator/api/v1"
)

func httpEndpoint(name, service string, port int32, path string) v1.Endpoint {
	return v1.Endpoint{Name: name, Service: service, Port: port, PathPrefix: path, Protocol: v1.EndpointProtocolHTTP}
}

// standaloneEndpoints are the endpoints of a cluster whose apps run beside
// the brokers, sorted by name as the operator publishes them.
func standaloneEndpoints() []v1.Endpoint {
	return []v1.Endpoint{
		httpEndpoint("admin", "orders-admin", 8080, "/admin"),
		httpEndpoint("connectors", "orders-connectors", 8080, "/inbound"),
		httpEndpoint("default", "orders-zeebe-gateway", 8080, "/"),
		{Name: "grpc", Service: "orders-zeebe-gateway", Port: 26500, Protocol: v1.EndpointProtocolGRPC},
		httpEndpoint("operate", "orders-operate", 8080, "/operate"),
		httpEndpoint("rest", "orders-zeebe-gateway", 8080, "/v2"),
		httpEndpoint("tasklist", "orders-tasklist", 8080, "/tasklist"),
	}
}

// embeddedEndpoints are the endpoints of a cluster whose apps run in the
// brokers.
func embeddedEndpoints() []v1.Endpoint {
	return []v1.Endpoint{
		httpEndpoint("admin", "orders-zeebe-client", 8080, "/admin"),
		httpEndpoint("default", "orders-zeebe-client", 8080, "/"),
		{Name: "grpc", Service: "orders-zeebe-client", Port: 26500, Protocol: v1.EndpointProtocolGRPC},
		httpEndpoint("operate", "orders-zeebe-client", 8080, "/operate"),
		httpEndpoint("rest", "orders-zeebe-client", 8080, "/v2"),
		httpEndpoint("tasklist", "orders-zeebe-client", 8080, "/tasklist"),
	}
}

func optimizeEndpoints() []v1.Endpoint {
	return []v1.Endpoint{httpEndpoint("optimize", "orders-optimize-webapp", 8090, "/")}
}

func planeEndpoints() []v1.Endpoint {
	return []v1.Endpoint{
		httpEndpoint("console", "orders-console", 8080, "/console"),
		httpEndpoint("identity", "orders-identity", 8084, "/identity"),
		httpEndpoint("keycloak", "orders-keycloak", 8080, "/auth"),
		httpEndpoint("web-modeler", "orders-web-modeler-webapp", 8070, "/modeler"),
		httpEndpoint("web-modeler-websockets", "orders-web-modeler-websockets", 8060, "/modeler-ws"),
	}
}

func testHosts(t *testing.T) Hosts {
	t.Helper()
	h, err := RenderHosts(validParams(), "orders", "team-a")
	require.NoError(t, err)
	return h
}

func ruleOf(t *testing.T, rule gatewayv1.HTTPRouteRule) (path, service string, port int32) {
	t.Helper()
	require.Len(t, rule.Matches, 1)
	require.Len(t, rule.BackendRefs, 1)
	match := rule.Matches[0].Path
	require.NotNil(t, match)
	assert.Equal(t, gatewayv1.PathMatchPathPrefix, *match.Type)

	ref := rule.BackendRefs[0].BackendObjectReference
	assert.Nil(t, ref.Namespace)
	return *match.Value, string(ref.Name), *ref.Port
}

func routesByKind(t *testing.T, objs []client.Object) (*gatewayv1.HTTPRoute, *gatewayv1.GRPCRoute) {
	t.Helper()
	var http *gatewayv1.HTTPRoute
	var grpc *gatewayv1.GRPCRoute
	for _, obj := range objs {
		switch r := obj.(type) {
		case *gatewayv1.HTTPRoute:
			http = r
		case *gatewayv1.GRPCRoute:
			grpc = r
		default:
			t.Errorf("unexpected object %T", obj)
		}
	}
	return http, grpc
}

func TestClusterRoutes_StandaloneAppsGetOneRuleForEachHTTPEndpoint(t *testing.T) {
	p := validParams()
	http, grpc := routesByKind(t, ClusterRoutes("orders-access", p, testHosts(t), standaloneEndpoints()))
	require.NotNil(t, http)
	require.NotNil(t, grpc)

	assert.Equal(t, "orders-access", http.Name)
	assert.Empty(t, http.Namespace)
	assert.Equal(t, "gateway.networking.k8s.io/v1", http.APIVersion)
	assert.Equal(t, "HTTPRoute", http.Kind)
	assert.Equal(t, []gatewayv1.Hostname{"orders.team-a.example.com"}, http.Spec.Hostnames)
	assert.Equal(t, []gatewayv1.ParentReference{ParentRef(p)}, http.Spec.ParentRefs)
	require.Len(t, http.Spec.Rules, 6)

	paths := make([]string, 0, len(http.Spec.Rules))
	for _, rule := range http.Spec.Rules {
		path, _, _ := ruleOf(t, rule)
		paths = append(paths, path)
	}
	assert.Equal(t, []string{"/admin", "/inbound", "/", "/operate", "/v2", "/tasklist"}, paths)

	path, service, port := ruleOf(t, http.Spec.Rules[3])
	assert.Equal(t, "/operate", path)
	assert.Equal(t, "orders-operate", service)
	assert.Equal(t, int32(8080), port)

	assert.Equal(t, "orders-access-grpc", grpc.Name)
	assert.Equal(t, "GRPCRoute", grpc.Kind)
	assert.Equal(t, []gatewayv1.Hostname{"grpc-orders.team-a.example.com"}, grpc.Spec.Hostnames)
	assert.Equal(t, []gatewayv1.ParentReference{ParentRef(p)}, grpc.Spec.ParentRefs)
	require.Len(t, grpc.Spec.Rules, 1)
	rule := grpc.Spec.Rules[0]
	assert.Empty(t, rule.Matches)
	require.Len(t, rule.BackendRefs, 1)
	assert.Equal(t, gatewayv1.ObjectName("orders-zeebe-gateway"), rule.BackendRefs[0].Name)
	assert.Equal(t, gatewayv1.PortNumber(26500), *rule.BackendRefs[0].Port)
}

func TestClusterRoutes_EmbeddedAppsRouteEveryPathToTheClientService(t *testing.T) {
	http, _ := routesByKind(t, ClusterRoutes("orders-access", validParams(), testHosts(t), embeddedEndpoints()))
	require.NotNil(t, http)
	require.Len(t, http.Spec.Rules, 5)
	for _, rule := range http.Spec.Rules {
		_, service, _ := ruleOf(t, rule)
		assert.Equal(t, "orders-zeebe-client", service)
	}
}

func TestClusterRoutes_EmptyPathPrefixMatchesTheRoot(t *testing.T) {
	endpoints := []v1.Endpoint{httpEndpoint("default", "orders-zeebe-gateway", 8080, "")}
	http, _ := routesByKind(t, ClusterRoutes("orders-access", validParams(), testHosts(t), endpoints))
	require.NotNil(t, http)
	path, _, _ := ruleOf(t, http.Spec.Rules[0])
	assert.Equal(t, "/", path)
}

func TestClusterRoutes_NoGRPCEndpointGivesNoGRPCRoute(t *testing.T) {
	endpoints := []v1.Endpoint{httpEndpoint("default", "orders-zeebe-gateway", 8080, "/")}
	objs := ClusterRoutes("orders-access", validParams(), testHosts(t), endpoints)
	http, grpc := routesByKind(t, objs)
	assert.NotNil(t, http)
	assert.Nil(t, grpc)
	assert.Len(t, objs, 1)
}

func TestClusterRoutes_NoEndpointsGiveNoRoutes(t *testing.T) {
	assert.Empty(t, ClusterRoutes("orders-access", validParams(), testHosts(t), nil))
}

func TestOptimizeRoute_RoutesTheOptimizeHost(t *testing.T) {
	obj := OptimizeRoute("orders-access", validParams(), testHosts(t), optimizeEndpoints())
	http, ok := obj.(*gatewayv1.HTTPRoute)
	require.True(t, ok)
	assert.Equal(t, "orders-access-optimize", http.Name)
	assert.Equal(t, []gatewayv1.Hostname{"optimize-orders.team-a.example.com"}, http.Spec.Hostnames)
	require.Len(t, http.Spec.Rules, 1)
	path, service, port := ruleOf(t, http.Spec.Rules[0])
	assert.Equal(t, "/", path)
	assert.Equal(t, "orders-optimize-webapp", service)
	assert.Equal(t, int32(8090), port)
}

func TestOptimizeRoute_NoneWithoutAnHTTPEndpoint(t *testing.T) {
	assert.Nil(t, OptimizeRoute("orders-access", validParams(), testHosts(t), nil))
}

func TestManagementRoute_OneRuleForEachPlaneEndpoint(t *testing.T) {
	obj := ManagementRoute("platform-access", validParams(), testHosts(t), planeEndpoints())
	http, ok := obj.(*gatewayv1.HTTPRoute)
	require.True(t, ok)
	assert.Equal(t, "platform-access", http.Name)
	assert.Equal(t, []gatewayv1.Hostname{"orders.platform.example.com"}, http.Spec.Hostnames)
	require.Len(t, http.Spec.Rules, 5)

	path, service, _ := ruleOf(t, http.Spec.Rules[2])
	assert.Equal(t, "/auth", path)
	assert.Equal(t, "orders-keycloak", service)
	path, _, _ = ruleOf(t, http.Spec.Rules[4])
	assert.Equal(t, "/modeler-ws", path)
}

func TestManagementRoute_NoneWithoutAnHTTPEndpoint(t *testing.T) {
	assert.Nil(t, ManagementRoute("platform-access", validParams(), testHosts(t), nil))
}

func TestParentRef_SetsTheListenerOnlyWhenNamed(t *testing.T) {
	p := validParams()
	ref := ParentRef(p)
	assert.Equal(t, gatewayv1.Group("gateway.networking.k8s.io"), *ref.Group)
	assert.Equal(t, gatewayv1.Kind("Gateway"), *ref.Kind)
	assert.Equal(t, gatewayv1.Namespace("gateway-system"), *ref.Namespace)
	assert.Equal(t, gatewayv1.ObjectName("public"), ref.Name)
	assert.Nil(t, ref.SectionName)

	p.ListenerName = httpsListener
	assert.Equal(t, ptr.To(gatewayv1.SectionName(httpsListener)), ParentRef(p).SectionName)
}
