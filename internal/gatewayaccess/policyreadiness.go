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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

// PolicyWaiting returns how many policies of desired controller accepted,
// and why the others are not. A policy counts when its live copy is not
// deleting, holds the spec of desired, has a status.ancestors entry of
// controller, and each entry of controller reports Accepted=True for the
// current generation. The live copy can hold more fields than desired: the
// API server defaults them. The ancestor of an entry does not matter, and the
// entries of another controller do not count. waiting is nil when every
// policy counts. Otherwise it describes the first policy, by kind and then by
// name, that does not: the reason Pending until controller reports on it, or
// the reason and the message of its False condition.
func PolicyWaiting(
	desired []*unstructured.Unstructured,
	live map[PolicyKey]*unstructured.Unstructured,
	controller gatewayv1.GatewayController,
) (accepted int, waiting *pluginkit.Waiting) {
	sorted := slices.Clone(desired)
	slices.SortFunc(sorted, func(a, b *unstructured.Unstructured) int {
		return cmp.Or(cmp.Compare(a.GetKind(), b.GetKind()), cmp.Compare(a.GetName(), b.GetName()))
	})

	for _, obj := range sorted {
		w := policyWaiting(obj, live[KeyOfPolicy(obj)], controller)
		if w == nil {
			accepted++
		} else if waiting == nil {
			waiting = w
		}
	}

	return accepted, waiting
}

func policyWaiting(desired, obj *unstructured.Unstructured, controller gatewayv1.GatewayController) *pluginkit.Waiting {
	pending := &pluginkit.Waiting{
		Reason: string(gatewayv1.RouteReasonPending),
		Message: fmt.Sprintf(
			"%s %s waits for the controller %s to accept it",
			desired.GetKind(),
			desired.GetName(),
			controller,
		),
	}
	if obj == nil {
		return pending
	}

	// The conditions of a live copy with another spec are about that spec,
	// until the apply of this reconcile gives the policy a new generation.
	if !containsJSON(desired.Object["spec"], obj.Object["spec"]) {
		return &pluginkit.Waiting{
			Reason: string(gatewayv1.RouteReasonPending),
			Message: fmt.Sprintf(
				"%s %s differs from the policy that the plugin applies",
				desired.GetKind(),
				desired.GetName(),
			),
		}
	}

	if !obj.GetDeletionTimestamp().IsZero() {
		return pending
	}

	var status gatewayv1.PolicyStatus
	if raw, ok := obj.Object["status"].(map[string]any); ok {
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw, &status); err != nil {
			return pending
		}
	}

	var conds []*metav1.Condition
	for i := range status.Ancestors {
		if status.Ancestors[i].ControllerName != controller {
			continue
		}

		cond := meta.FindStatusCondition(status.Ancestors[i].Conditions, string(gatewayv1.PolicyConditionAccepted))
		if cond == nil || cond.ObservedGeneration != obj.GetGeneration() {
			return pending
		}
		conds = append(conds, cond)
	}
	if len(conds) == 0 {
		return pending
	}

	for _, cond := range conds {
		if cond.Status != metav1.ConditionTrue {
			return &pluginkit.Waiting{
				Reason:  cond.Reason,
				Message: fmt.Sprintf("%s %s: %s", desired.GetKind(), desired.GetName(), cond.Message),
			}
		}
	}

	return nil
}

// containsJSON reports whether got holds want: each key of a map with a value
// that holds the value of want, each item of a list of the same length, and
// each number by value, whether it decoded as an int64 or a float64.
func containsJSON(want, got any) bool {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return false
		}
		for k, v := range w {
			if !containsJSON(v, g[k]) {
				return false
			}
		}
		return true
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			return false
		}
		for i := range w {
			if !containsJSON(w[i], g[i]) {
				return false
			}
		}
		return true
	case int64:
		return sameNumber(float64(w), got)
	case float64:
		return sameNumber(w, got)
	}
	return want == got
}

func sameNumber(want float64, got any) bool {
	switch g := got.(type) {
	case int64:
		return float64(g) == want
	case float64:
		return g == want
	}
	return false
}
