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
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
	v1 "github.com/konsole-is/camunda-operator/api/v1"

	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

// The target kinds that the plugin serves, and the kind whose routes it adds
// to those of a cluster.
const (
	kindCamundaCluster           = "CamundaCluster"
	kindCamundaManagementCluster = "CamundaManagementCluster"
	kindCamundaOptimize          = "CamundaOptimize"
)

// The events that the plugin records on a request.
const (
	eventReasonRouteCreated = "RouteCreated"
	eventReasonRouteDeleted = "RouteDeleted"
	eventActionCreate       = "Create"
	eventActionDelete       = "Delete"
)

// Plugin serves access requests. It publishes the four outputs of the
// hostname patterns, and it writes an HTTPRoute and a GRPCRoute for a
// CamundaCluster and its CamundaOptimize, or an HTTPRoute for a
// CamundaManagementCluster, from status.endpoints of the target. It deletes
// the routes of a request that it no longer wants, and every route of a
// deleted request.
type Plugin struct {
	client client.Client
	// reader reads the Gateway, its GatewayClass, and the certificate
	// Secret, which the cache does not hold.
	reader                  client.Reader
	reportCertificateExpiry bool
	now                     func() time.Time
}

// access is what a request routes: the host of its summary, its routes, and
// a link for each HTTP endpoint of the routes.
type access struct {
	host     string
	routes   []client.Object
	links    []pluginsv1alpha1.Link
	notReady *pluginkit.Waiting
}

// Reconcile returns the outputs, the routes, and the status of the request.
// It is Pending with the reason InvalidParameters when a pattern gives no
// valid host, and with the reason UnsupportedTarget for a target kind that
// the plugin does not serve. Ready is False with the reason
// WaitingForEndpoints until the target publishes endpoints, and with the
// reason GatewayNotFound while the Gateway of the parameters or its
// GatewayClass does not exist. Then it follows that Gateway.
func (p *Plugin) Reconcile(
	ctx context.Context,
	req pluginkit.Request[Params, map[string]string],
) (pluginkit.Result, error) {
	hosts, err := RenderHosts(req.Parameters, req.Object.Spec.ClaimRef.Name, req.Object.Namespace)
	if err != nil {
		return pluginkit.Result{
			Pending: &pluginkit.Waiting{Reason: pluginsv1alpha1.ReasonInvalidParameters, Message: err.Error()},
		}, nil
	}

	var a access
	switch target := req.Object.Spec.Target; target.Kind {
	case kindCamundaCluster:
		a, err = p.clusterAccess(ctx, req, hosts)
	case kindCamundaManagementCluster:
		a, err = p.planeAccess(ctx, req, hosts)
	default:
		return pluginkit.Result{Pending: &pluginkit.Waiting{
			Reason: ReasonUnsupportedTarget,
			Message: fmt.Sprintf(
				"the gateway-access plugin serves the target kinds %s and %s, not %s",
				kindCamundaCluster,
				kindCamundaManagementCluster,
				target.Kind,
			),
		}}, nil
	}
	if err != nil {
		return pluginkit.Result{}, err
	}

	live, err := listRoutes(ctx, p.client, req.Object)
	if err != nil {
		return pluginkit.Result{}, err
	}

	if err := deleteRoutes(ctx, p.client, req, StaleRoutes(live, a.routes), "whose endpoint is gone"); err != nil {
		return pluginkit.Result{}, err
	}

	liveByKey := make(map[RouteKey]client.Object, len(live))
	for _, obj := range live {
		liveByKey[KeyOf(obj)] = obj
	}

	resources := make([]pluginsv1alpha1.ResourceRef, 0, len(a.routes))
	for _, obj := range a.routes {
		key := KeyOf(obj)
		host := routeHost(obj)
		if _, ok := liveByKey[key]; !ok {
			req.Event(
				corev1.EventTypeNormal,
				eventReasonRouteCreated,
				eventActionCreate,
				"Applying the %s %s for the host %s",
				key.Kind,
				key.Name,
				host,
			)
		}
		resources = append(resources, pluginsv1alpha1.ResourceRef{
			Kind:     key.Kind,
			Name:     key.Name,
			Role:     "Routes " + host,
			OnOrphan: pluginsv1alpha1.OnOrphanDeleted,
		})
	}

	controller, noGateway, err := GatewayController(ctx, p.reader, req.Parameters)
	if err != nil {
		return pluginkit.Result{}, err
	}

	accepted, waiting := RouteWaiting(a.routes, liveByKey, ParentRef(req.Parameters), controller)
	notReady := cmp.Or(a.notReady, noGateway, waiting)

	res := pluginkit.Result{
		Outputs:  Outputs(req.Parameters, hosts),
		Objects:  a.routes,
		NotReady: notReady,
		Summary: []pluginsv1alpha1.SummaryItem{
			{Label: "Host", Value: a.host},
			{Label: "Routes accepted", Value: fmt.Sprintf("%d of %d", accepted, len(a.routes))},
		},
		Links:     a.links,
		Resources: resources,
	}

	if p.reportCertificateExpiry {
		if item := CertificateItem(ctx, p.reader, req.Parameters, p.now()); item != nil {
			res.Summary = append(res.Summary, *item)
		}
	}

	return res, nil
}

// clusterAccess returns the routes of a CamundaCluster target and of the
// first CamundaOptimize, by name, that reads it and publishes endpoints.
func (p *Plugin) clusterAccess(
	ctx context.Context,
	req pluginkit.Request[Params, map[string]string],
	hosts Hosts,
) (access, error) {
	name := req.Object.Spec.Target.Name
	a := access{host: hosts.Cluster}

	var cluster v1.CamundaCluster
	err := p.client.Get(ctx, client.ObjectKey{Namespace: req.Object.Namespace, Name: name}, &cluster)
	switch {
	case apierrors.IsNotFound(err) || (err == nil && len(cluster.Status.Endpoints) == 0):
		a.notReady = waitingForEndpoints(kindCamundaCluster, name)
		return a, nil
	case err != nil:
		return access{}, fmt.Errorf("reading the CamundaCluster %s: %w", name, err)
	}

	a.routes = ClusterRoutes(req.Object.Name, req.Parameters, hosts, cluster.Status.Endpoints)
	a.links = links(req.Parameters, hosts.Cluster, cluster.Status.Endpoints)

	var optimizes v1.CamundaOptimizeList
	if err := p.client.List(ctx, &optimizes, client.InNamespace(req.Object.Namespace)); err != nil {
		return access{}, fmt.Errorf("listing the CamundaOptimizes of the namespace: %w", err)
	}
	slices.SortFunc(optimizes.Items, func(a, b v1.CamundaOptimize) int { return strings.Compare(a.Name, b.Name) })

	for _, o := range optimizes.Items {
		if o.Spec.ClusterRef.Name != name || len(o.Status.Endpoints) == 0 {
			continue
		}

		if route := OptimizeRoute(req.Object.Name, req.Parameters, hosts, o.Status.Endpoints); route != nil {
			a.routes = append(a.routes, route)
			a.links = append(a.links, links(req.Parameters, hosts.Optimize, o.Status.Endpoints)...)
		}
		break
	}

	return a, nil
}

// planeAccess returns the route of a CamundaManagementCluster target.
func (p *Plugin) planeAccess(
	ctx context.Context,
	req pluginkit.Request[Params, map[string]string],
	hosts Hosts,
) (access, error) {
	name := req.Object.Spec.Target.Name
	a := access{host: hosts.Management}

	var plane v1.CamundaManagementCluster
	err := p.client.Get(ctx, client.ObjectKey{Namespace: req.Object.Namespace, Name: name}, &plane)
	switch {
	case apierrors.IsNotFound(err) || (err == nil && len(plane.Status.Endpoints) == 0):
		a.notReady = waitingForEndpoints(kindCamundaManagementCluster, name)
		return a, nil
	case err != nil:
		return access{}, fmt.Errorf("reading the CamundaManagementCluster %s: %w", name, err)
	}

	if route := ManagementRoute(req.Object.Name, req.Parameters, hosts, plane.Status.Endpoints); route != nil {
		a.routes = []client.Object{route}
		a.links = links(req.Parameters, hosts.Management, plane.Status.Endpoints)
	}

	return a, nil
}

func waitingForEndpoints(kind, name string) *pluginkit.Waiting {
	return &pluginkit.Waiting{
		Reason:  pluginsv1alpha1.ReasonWaitingForEndpoints,
		Message: fmt.Sprintf("%s %s has published no endpoints yet", kind, name),
	}
}

// links returns one link for each HTTP endpoint of endpoints on host.
func links(p Params, host string, endpoints []v1.Endpoint) []pluginsv1alpha1.Link {
	var out []pluginsv1alpha1.Link
	for _, e := range endpoints {
		if e.Protocol == v1.EndpointProtocolHTTP {
			out = append(out, pluginsv1alpha1.Link{Name: e.Name, URL: p.scheme() + "://" + host + e.PathPrefix})
		}
	}
	return out
}

// listRoutes lists the HTTPRoutes and the GRPCRoutes that carry the labels of
// request.
func listRoutes(
	ctx context.Context,
	reader client.Reader,
	request *pluginsv1alpha1.PluginRequest,
) ([]client.Object, error) {
	opts := []client.ListOption{
		client.InNamespace(request.Namespace),
		client.MatchingLabels{
			pluginsv1alpha1.LabelRegistration: request.Spec.Registration,
			pluginsv1alpha1.LabelRequest:      request.Name,
		},
	}

	var httpRoutes gatewayv1.HTTPRouteList
	if err := reader.List(ctx, &httpRoutes, opts...); err != nil {
		return nil, fmt.Errorf("listing the HTTPRoutes of the request: %w", err)
	}

	var grpcRoutes gatewayv1.GRPCRouteList
	if err := reader.List(ctx, &grpcRoutes, opts...); err != nil {
		return nil, fmt.Errorf("listing the GRPCRoutes of the request: %w", err)
	}

	routes := make([]client.Object, 0, len(httpRoutes.Items)+len(grpcRoutes.Items))
	for i := range httpRoutes.Items {
		routes = append(routes, &httpRoutes.Items[i])
	}
	for i := range grpcRoutes.Items {
		routes = append(routes, &grpcRoutes.Items[i])
	}
	return routes, nil
}

// StaleRoutes returns the routes of live whose kind and name are not in
// desired.
func StaleRoutes(live, desired []client.Object) []client.Object {
	wanted := make(map[RouteKey]bool, len(desired))
	for _, obj := range desired {
		wanted[KeyOf(obj)] = true
	}

	var stale []client.Object
	for _, obj := range live {
		if !wanted[KeyOf(obj)] {
			stale = append(stale, obj)
		}
	}
	return stale
}

// deleteRoutes deletes routes, each with an event on the request that ends
// with why. A route that is already gone is not an error.
func deleteRoutes(
	ctx context.Context,
	c client.Client,
	req pluginkit.Request[Params, map[string]string],
	routes []client.Object,
	why string,
) error {
	for _, obj := range routes {
		key := KeyOf(obj)
		// The UID keeps a route that replaced obj under its name.
		uid := obj.GetUID()
		if err := c.Delete(ctx, obj, client.Preconditions{UID: &uid}); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("deleting the %s %s: %w", key.Kind, key.Name, err)
		}
		req.Event(
			corev1.EventTypeNormal,
			eventReasonRouteDeleted,
			eventActionDelete,
			"Deleted the %s %s, %s",
			key.Kind,
			key.Name,
			why,
		)
	}
	return nil
}

func routeHost(obj client.Object) string {
	var hostnames []gatewayv1.Hostname
	switch r := obj.(type) {
	case *gatewayv1.HTTPRoute:
		hostnames = r.Spec.Hostnames
	case *gatewayv1.GRPCRoute:
		hostnames = r.Spec.Hostnames
	}

	if len(hostnames) == 0 {
		return ""
	}
	return string(hostnames[0])
}

// Delete deletes every route of the request. It returns an error while a
// route is still there, so that pluginkit keeps the finalizer and retries.
func (p *Plugin) Delete(ctx context.Context, req pluginkit.Request[Params, map[string]string]) error {
	live, err := listRoutes(ctx, p.reader, req.Object)
	if err != nil {
		return err
	}

	toDelete := slices.DeleteFunc(live, func(obj client.Object) bool { return !obj.GetDeletionTimestamp().IsZero() })
	if err := deleteRoutes(ctx, p.client, req, toDelete, "which goes with the request"); err != nil {
		return err
	}

	// A route that a finalizer holds is still there after its delete.
	left, err := listRoutes(ctx, p.reader, req.Object)
	if err != nil {
		return err
	}

	names := make([]string, 0, len(left))
	for _, obj := range left {
		key := KeyOf(obj)
		names = append(names, key.Kind+" "+key.Name)
	}
	if len(names) > 0 {
		return fmt.Errorf("waiting for the routes %s to be gone", strings.Join(names, ", "))
	}
	return nil
}

// Orphan deletes every route of the request, as Delete does. A route to a
// Service that goes with the target never stays.
func (p *Plugin) Orphan(ctx context.Context, req pluginkit.Request[Params, map[string]string]) error {
	return p.Delete(ctx, req)
}
