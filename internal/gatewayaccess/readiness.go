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
	"fmt"
	"slices"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

// RouteKey identifies a route of a request. An HTTPRoute and a GRPCRoute
// with one name are two routes.
type RouteKey struct {
	Kind string
	Name string
}

// KeyOf returns the key of an HTTPRoute or a GRPCRoute. The kind comes from
// the Go type, so obj needs no TypeMeta.
func KeyOf(obj client.Object) RouteKey {
	kind := ""
	switch obj.(type) {
	case *gatewayv1.HTTPRoute:
		kind = "HTTPRoute"
	case *gatewayv1.GRPCRoute:
		kind = "GRPCRoute"
	}
	return RouteKey{Kind: kind, Name: obj.GetName()}
}

// RouteWaiting returns how many routes of desired the Gateway of parent
// accepted, and why the others are not. A route counts when its live copy is
// not deleting, has the spec of desired, and reports Accepted=True and
// ResolvedRefs=True for parent from controller, both for its current
// generation. The entries of another controller do not count: a Gateway
// recreated under another controller leaves them on the route. waiting is
// nil when every route counts. Otherwise it describes the first route, by
// kind and then by name, that does not: the reason Pending until the Gateway
// reports on it, or the reason and the message of its False condition.
func RouteWaiting(
	desired []client.Object,
	live map[RouteKey]client.Object,
	parent gatewayv1.ParentReference,
	controller gatewayv1.GatewayController,
) (accepted int, waiting *pluginkit.Waiting) {
	sorted := slices.Clone(desired)
	slices.SortFunc(sorted, func(a, b client.Object) int {
		ka, kb := KeyOf(a), KeyOf(b)
		return cmp.Or(cmp.Compare(ka.Kind, kb.Kind), cmp.Compare(ka.Name, kb.Name))
	})

	for _, obj := range sorted {
		w := routeWaiting(obj, live[KeyOf(obj)], parent, controller)
		if w == nil {
			accepted++
		} else if waiting == nil {
			waiting = w
		}
	}

	return accepted, waiting
}

func routeWaiting(
	desired, obj client.Object,
	parent gatewayv1.ParentReference,
	controller gatewayv1.GatewayController,
) *pluginkit.Waiting {
	key := KeyOf(desired)
	pending := &pluginkit.Waiting{
		Reason: string(gatewayv1.RouteReasonPending),
		Message: fmt.Sprintf(
			"%s %s waits for the Gateway %s/%s to accept it",
			key.Kind,
			key.Name,
			ptr.Deref(parent.Namespace, ""),
			parent.Name,
		),
	}
	if obj == nil {
		return pending
	}

	// The conditions of a live copy with another spec are about that spec,
	// until the apply of this reconcile gives the route a new generation.
	if !sameSpec(desired, obj) {
		return &pluginkit.Waiting{
			Reason:  string(gatewayv1.RouteReasonPending),
			Message: fmt.Sprintf("%s %s differs from the route that the plugin applies", key.Kind, key.Name),
		}
	}

	if !obj.GetDeletionTimestamp().IsZero() {
		return pending
	}

	entry := parentStatus(obj, parent, controller)
	if entry == nil {
		return pending
	}

	for _, conditionType := range []gatewayv1.RouteConditionType{
		gatewayv1.RouteConditionAccepted,
		gatewayv1.RouteConditionResolvedRefs,
	} {
		cond := meta.FindStatusCondition(entry.Conditions, string(conditionType))
		switch {
		case cond == nil, cond.ObservedGeneration != obj.GetGeneration():
			return pending
		case cond.Status != metav1.ConditionTrue:
			return &pluginkit.Waiting{
				Reason:  cond.Reason,
				Message: fmt.Sprintf("%s %s: %s", key.Kind, key.Name, cond.Message),
			}
		}
	}

	return nil
}

// sameSpec reports whether the live route has the spec of the built route.
// The API server defaults the group, the kind, and the weight of a backend,
// which the built route leaves unset.
func sameSpec(built, live client.Object) bool {
	switch b := built.(type) {
	case *gatewayv1.HTTPRoute:
		l, ok := live.(*gatewayv1.HTTPRoute)
		if !ok {
			return false
		}
		want := b.Spec.DeepCopy()
		for i := range want.Rules {
			for j := range want.Rules[i].BackendRefs {
				setBackendDefaults(&want.Rules[i].BackendRefs[j].BackendRef)
			}
		}
		return equality.Semantic.DeepEqual(*want, l.Spec)
	case *gatewayv1.GRPCRoute:
		l, ok := live.(*gatewayv1.GRPCRoute)
		if !ok {
			return false
		}
		want := b.Spec.DeepCopy()
		for i := range want.Rules {
			for j := range want.Rules[i].BackendRefs {
				setBackendDefaults(&want.Rules[i].BackendRefs[j].BackendRef)
			}
		}
		return equality.Semantic.DeepEqual(*want, l.Spec)
	}
	return false
}

func setBackendDefaults(ref *gatewayv1.BackendRef) {
	if ref.Group == nil {
		ref.Group = new(gatewayv1.Group(""))
	}
	if ref.Kind == nil {
		ref.Kind = new(gatewayv1.Kind("Service"))
	}
	if ref.Weight == nil {
		ref.Weight = new(int32(1))
	}
}

// parentStatus returns the status.parents entry of obj that controller wrote
// for parent, or nil.
func parentStatus(
	obj client.Object,
	parent gatewayv1.ParentReference,
	controller gatewayv1.GatewayController,
) *gatewayv1.RouteParentStatus {
	var parents []gatewayv1.RouteParentStatus
	switch r := obj.(type) {
	case *gatewayv1.HTTPRoute:
		parents = r.Status.Parents
	case *gatewayv1.GRPCRoute:
		parents = r.Status.Parents
	}

	for i := range parents {
		if parents[i].ControllerName == controller && sameParent(parents[i].ParentRef, parent, obj.GetNamespace()) {
			return &parents[i]
		}
	}
	return nil
}

// sameParent compares two parent references of a route in namespace. An
// unset group, kind, or namespace takes its Gateway API default.
func sameParent(a, b gatewayv1.ParentReference, namespace string) bool {
	group := func(r gatewayv1.ParentReference) gatewayv1.Group {
		return ptr.Deref(r.Group, gatewayv1.GroupName)
	}
	kind := func(r gatewayv1.ParentReference) gatewayv1.Kind {
		return ptr.Deref(r.Kind, "Gateway")
	}
	ns := func(r gatewayv1.ParentReference) gatewayv1.Namespace {
		return ptr.Deref(r.Namespace, gatewayv1.Namespace(namespace))
	}

	return group(a) == group(b) &&
		kind(a) == kind(b) &&
		ns(a) == ns(b) &&
		a.Name == b.Name &&
		ptr.Deref(a.SectionName, "") == ptr.Deref(b.SectionName, "")
}
