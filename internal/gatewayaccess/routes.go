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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	v1 "github.com/konsole-is/camunda-operator/api/v1"
)

// The suffixes of the route names, after the name of the request.
const (
	grpcRouteSuffix     = "-grpc"
	optimizeRouteSuffix = "-optimize"
)

// ClusterRoutes returns the routes of a cluster for the request named
// request: the HTTPRoute <request> on h.Cluster with one rule for each HTTP
// endpoint, and the GRPCRoute <request>-grpc on h.GRPC with one rule for each
// gRPC endpoint. A route without a rule is left out. The routes have no
// namespace and no labels.
func ClusterRoutes(request string, p Params, h Hosts, endpoints []v1.Endpoint) []client.Object {
	var routes []client.Object
	if route := httpRoute(request, p, h.Cluster, endpoints); route != nil {
		routes = append(routes, route)
	}

	if route := grpcRoute(request+grpcRouteSuffix, p, h.GRPC, endpoints); route != nil {
		routes = append(routes, route)
	}

	return routes
}

// OptimizeRoute returns the HTTPRoute <request>-optimize on h.Optimize, or
// nil when endpoints has no HTTP endpoint.
func OptimizeRoute(request string, p Params, h Hosts, endpoints []v1.Endpoint) client.Object {
	return asObject(httpRoute(request+optimizeRouteSuffix, p, h.Optimize, endpoints))
}

// ManagementRoute returns the HTTPRoute <request> on h.Management, or nil
// when endpoints has no HTTP endpoint.
func ManagementRoute(request string, p Params, h Hosts, endpoints []v1.Endpoint) client.Object {
	return asObject(httpRoute(request, p, h.Management, endpoints))
}

// asObject keeps a nil route a nil interface.
func asObject(route *gatewayv1.HTTPRoute) client.Object {
	if route == nil {
		return nil
	}
	return route
}

// ParentRef is the parent of every route of a request: the Gateway of
// p.GatewayRef, and its listener p.ListenerName when that is set.
func ParentRef(p Params) gatewayv1.ParentReference {
	ref := gatewayv1.ParentReference{
		Group:     ptr.To(gatewayv1.Group(gatewayv1.GroupName)),
		Kind:      ptr.To(gatewayv1.Kind("Gateway")),
		Namespace: new(gatewayv1.Namespace(p.GatewayRef.Namespace)),
		Name:      gatewayv1.ObjectName(p.GatewayRef.Name),
	}
	if p.ListenerName != "" {
		ref.SectionName = new(gatewayv1.SectionName(p.ListenerName))
	}
	return ref
}

func httpRoute(name string, p Params, host string, endpoints []v1.Endpoint) *gatewayv1.HTTPRoute {
	var rules []gatewayv1.HTTPRouteRule
	for _, e := range endpoints {
		if e.Protocol != v1.EndpointProtocolHTTP {
			continue
		}

		path := e.PathPrefix
		if path == "" {
			path = "/"
		}
		rules = append(rules, gatewayv1.HTTPRouteRule{
			Matches: []gatewayv1.HTTPRouteMatch{{
				Path: &gatewayv1.HTTPPathMatch{
					Type:  ptr.To(gatewayv1.PathMatchPathPrefix),
					Value: new(path),
				},
			}},
			BackendRefs: []gatewayv1.HTTPBackendRef{{BackendRef: backendRef(e)}},
		})
	}
	if len(rules) == 0 {
		return nil
	}

	return &gatewayv1.HTTPRoute{
		TypeMeta:   metav1.TypeMeta{APIVersion: gatewayv1.GroupVersion.String(), Kind: "HTTPRoute"},
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: gatewayv1.HTTPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{ParentRefs: []gatewayv1.ParentReference{ParentRef(p)}},
			Hostnames:       []gatewayv1.Hostname{gatewayv1.Hostname(host)},
			Rules:           rules,
		},
	}
}

func grpcRoute(name string, p Params, host string, endpoints []v1.Endpoint) *gatewayv1.GRPCRoute {
	var rules []gatewayv1.GRPCRouteRule
	for _, e := range endpoints {
		if e.Protocol == v1.EndpointProtocolGRPC {
			rules = append(rules, gatewayv1.GRPCRouteRule{
				BackendRefs: []gatewayv1.GRPCBackendRef{{BackendRef: backendRef(e)}},
			})
		}
	}
	if len(rules) == 0 {
		return nil
	}

	return &gatewayv1.GRPCRoute{
		TypeMeta:   metav1.TypeMeta{APIVersion: gatewayv1.GroupVersion.String(), Kind: "GRPCRoute"},
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: gatewayv1.GRPCRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{ParentRefs: []gatewayv1.ParentReference{ParentRef(p)}},
			Hostnames:       []gatewayv1.Hostname{gatewayv1.Hostname(host)},
			Rules:           rules,
		},
	}
}

// backendRef sends traffic to the Service of e. It names no namespace: the
// Service is in the namespace of the route.
func backendRef(e v1.Endpoint) gatewayv1.BackendRef {
	return gatewayv1.BackendRef{
		BackendObjectReference: gatewayv1.BackendObjectReference{
			Name: gatewayv1.ObjectName(e.Service),
			Port: new(e.Port),
		},
	}
}
