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
	"sync"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// fakeGatewayController is the controller name of the fake Gateway.
const fakeGatewayController = "example.com/fake-gateway"

// startFakeGateway adds to mgr a controller that stands in for a Gateway
// implementation, which envtest does not have. For each parent of an
// HTTPRoute or a GRPCRoute that names a Gateway in gateway-system, it writes
// Accepted and ResolvedRefs as True for the generation of the route, or
// Accepted=False with the reason NotAllowedByListeners when refuse holds the
// name of the route.
func startFakeGateway(mgr ctrl.Manager, refuse *sync.Map) error {
	c := mgr.GetClient()

	err := ctrl.NewControllerManagedBy(mgr).
		Named("fake-gateway-httproute").
		For(&gatewayv1.HTTPRoute{}).
		Complete(reconcile.Func(func(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
			var route gatewayv1.HTTPRoute
			if err := c.Get(ctx, req.NamespacedName, &route); err != nil {
				return reconcile.Result{}, client.IgnoreNotFound(err)
			}
			return reconcile.Result{}, writeParents(
				ctx,
				c,
				&route,
				route.Spec.ParentRefs,
				&route.Status.RouteStatus,
				refuse,
			)
		}))
	if err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		Named("fake-gateway-grpcroute").
		For(&gatewayv1.GRPCRoute{}).
		Complete(reconcile.Func(func(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
			var route gatewayv1.GRPCRoute
			if err := c.Get(ctx, req.NamespacedName, &route); err != nil {
				return reconcile.Result{}, client.IgnoreNotFound(err)
			}
			return reconcile.Result{}, writeParents(
				ctx,
				c,
				&route,
				route.Spec.ParentRefs,
				&route.Status.RouteStatus,
				refuse,
			)
		}))
}

// writeParents sets status of route, whose parents are refs, and writes it
// when it changed.
func writeParents(
	ctx context.Context,
	c client.Client,
	route client.Object,
	refs []gatewayv1.ParentReference,
	status *gatewayv1.RouteStatus,
	refuse *sync.Map,
) error {
	want := status.DeepCopy()
	want.Parents = nil
	_, refused := refuse.Load(route.GetName())

	for _, ref := range refs {
		if ptr.Deref(ref.Namespace, "") != "gateway-system" {
			continue
		}

		entry := gatewayv1.RouteParentStatus{ParentRef: ref, ControllerName: fakeGatewayController}
		for _, existing := range status.Parents {
			if equality.Semantic.DeepEqual(existing.ParentRef, ref) {
				entry.Conditions = existing.Conditions
			}
		}

		accepted := metav1.Condition{
			Type:               string(gatewayv1.RouteConditionAccepted),
			Status:             metav1.ConditionTrue,
			Reason:             string(gatewayv1.RouteReasonAccepted),
			Message:            "The fake Gateway accepts the route",
			ObservedGeneration: route.GetGeneration(),
		}
		if refused {
			accepted.Status = metav1.ConditionFalse
			accepted.Reason = string(gatewayv1.RouteReasonNotAllowedByListeners)
			accepted.Message = "no listener admits the namespace"
		}
		meta.SetStatusCondition(&entry.Conditions, accepted)
		meta.SetStatusCondition(&entry.Conditions, metav1.Condition{
			Type:               string(gatewayv1.RouteConditionResolvedRefs),
			Status:             metav1.ConditionTrue,
			Reason:             string(gatewayv1.RouteReasonResolvedRefs),
			Message:            "The fake Gateway resolves every backend",
			ObservedGeneration: route.GetGeneration(),
		})
		want.Parents = append(want.Parents, entry)
	}

	if equality.Semantic.DeepEqual(status, want) {
		return nil
	}
	*status = *want
	return c.Status().Update(ctx, route)
}
