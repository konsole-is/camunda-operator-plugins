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
// ResolvedRefs=True for parent, both for its current generation. waiting is
// nil when every route counts. Otherwise it describes the first route, by
// kind and then by name, that does not: the reason Pending until the Gateway
// reports on it, or the reason and the message of its False condition.
func RouteWaiting(
	desired []client.Object,
	live map[RouteKey]client.Object,
	parent gatewayv1.ParentReference,
) (accepted int, waiting *pluginkit.Waiting) {
	sorted := slices.Clone(desired)
	slices.SortFunc(sorted, func(a, b client.Object) int {
		ka, kb := KeyOf(a), KeyOf(b)
		return cmp.Or(cmp.Compare(ka.Kind, kb.Kind), cmp.Compare(ka.Name, kb.Name))
	})

	for _, obj := range sorted {
		w := routeWaiting(obj, live[KeyOf(obj)], parent)
		if w == nil {
			accepted++
		} else if waiting == nil {
			waiting = w
		}
	}

	return accepted, waiting
}

func routeWaiting(desired, obj client.Object, parent gatewayv1.ParentReference) *pluginkit.Waiting {
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
	// The conditions of a live copy with another spec are about that spec,
	// until the apply of this reconcile gives the route a new generation.
	if obj == nil || !slices.Equal(routeShape(obj, obj.GetNamespace()), routeShape(desired, obj.GetNamespace())) {
		return pending
	}

	if !obj.GetDeletionTimestamp().IsZero() {
		return pending
	}

	entry := parentStatus(obj, parent)
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

// routeShape describes the fields of a route in namespace that the plugin
// sets, with the defaults of the API server filled in. A built route and a
// live copy of it have the same shape.
func routeShape(obj client.Object, namespace string) []string {
	var parents []gatewayv1.ParentReference
	var hostnames []gatewayv1.Hostname
	var shape []string
	switch r := obj.(type) {
	case *gatewayv1.HTTPRoute:
		parents, hostnames = r.Spec.ParentRefs, r.Spec.Hostnames
		for i, rule := range r.Spec.Rules {
			for _, m := range rule.Matches {
				var path gatewayv1.HTTPPathMatch
				if m.Path != nil {
					path = *m.Path
				}
				shape = append(shape, fmt.Sprintf(
					"rule %d path %s %s",
					i,
					ptr.Deref(path.Type, gatewayv1.PathMatchPathPrefix),
					ptr.Deref(path.Value, "/"),
				))
			}
			for _, b := range rule.BackendRefs {
				shape = append(shape, fmt.Sprintf("rule %d backend %s", i, backendShape(b.BackendRef)))
			}
		}
	case *gatewayv1.GRPCRoute:
		parents, hostnames = r.Spec.ParentRefs, r.Spec.Hostnames
		for i, rule := range r.Spec.Rules {
			shape = append(shape, fmt.Sprintf("rule %d matches %d", i, len(rule.Matches)))
			for _, b := range rule.BackendRefs {
				shape = append(shape, fmt.Sprintf("rule %d backend %s", i, backendShape(b.BackendRef)))
			}
		}
	}

	for _, h := range hostnames {
		shape = append(shape, "host "+string(h))
	}
	for _, p := range parents {
		shape = append(shape, fmt.Sprintf(
			"parent %s/%s/%s/%s/%s",
			ptr.Deref(p.Group, gatewayv1.GroupName),
			ptr.Deref(p.Kind, "Gateway"),
			ptr.Deref(p.Namespace, gatewayv1.Namespace(namespace)),
			p.Name,
			ptr.Deref(p.SectionName, ""),
		))
	}
	return shape
}

func backendShape(b gatewayv1.BackendRef) string {
	return fmt.Sprintf(
		"%s/%s/%s/%s:%d weight %d",
		ptr.Deref(b.Group, ""),
		ptr.Deref(b.Kind, "Service"),
		ptr.Deref(b.Namespace, ""),
		b.Name,
		ptr.Deref(b.Port, 0),
		ptr.Deref(b.Weight, 1),
	)
}

// parentStatus returns the status.parents entry of obj for parent, or nil.
func parentStatus(obj client.Object, parent gatewayv1.ParentReference) *gatewayv1.RouteParentStatus {
	var parents []gatewayv1.RouteParentStatus
	switch r := obj.(type) {
	case *gatewayv1.HTTPRoute:
		parents = r.Status.Parents
	case *gatewayv1.GRPCRoute:
		parents = r.Status.Parents
	}

	for i := range parents {
		if sameParent(parents[i].ParentRef, parent, obj.GetNamespace()) {
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
