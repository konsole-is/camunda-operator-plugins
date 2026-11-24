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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

func TestStaleRoutes_ReturnsRoutesThatAreNotWanted(t *testing.T) {
	http := func(name string) client.Object {
		return &gatewayv1.HTTPRoute{ObjectMeta: metav1.ObjectMeta{Name: name}}
	}
	grpc := func(name string) client.Object {
		return &gatewayv1.GRPCRoute{ObjectMeta: metav1.ObjectMeta{Name: name}}
	}

	live := []client.Object{http("a"), grpc("a-grpc"), http("a-optimize")}
	desired := []client.Object{http("a"), grpc("a-grpc")}
	assert.Equal(t, []client.Object{http("a-optimize")}, StaleRoutes(live, desired))

	// A GRPCRoute and an HTTPRoute with one name are two routes.
	assert.Equal(t, []client.Object{grpc("a")}, StaleRoutes([]client.Object{grpc("a")}, []client.Object{http("a")}))
	assert.Empty(t, StaleRoutes(live, live))
}

func TestDeleteRoutes_DeletesOnlyTheListedRoute(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, gatewayv1.Install(scheme))
	var preconditions *metav1.Preconditions
	// The fake client does not check preconditions, so the interceptor
	// answers as the API server does for a route that replaced the listed one.
	c := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
		Delete: func(_ context.Context, _ client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			var o client.DeleteOptions
			o.ApplyOptions(opts)
			preconditions = o.Preconditions
			return apierrors.NewConflict(schema.GroupResource{Resource: "httproutes"}, obj.GetName(), nil)
		},
	}).Build()
	listed := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "orders-access", UID: "listed"},
	}

	err := deleteRoutes(context.Background(), c, pluginkit.Request[Params, map[string]string]{},
		[]client.Object{listed}, "whose endpoint is gone")
	require.Error(t, err)
	assert.True(t, apierrors.IsConflict(err), err)
	require.NotNil(t, preconditions)
	assert.Equal(t, listed.UID, *preconditions.UID)
}

// policyTargeting returns a policy of the kind gvk named name whose target is
// the HTTPRoute route.
func policyTargeting(gvk schema.GroupVersionKind, name, route string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{
		"targetRefs": []any{map[string]any{"group": gatewayv1.GroupName, "kind": "HTTPRoute", "name": route}},
	}}}
	u.SetGroupVersionKind(gvk)
	u.SetName(name)
	return u
}

func TestStalePolicies_ReturnsPoliciesThatAreNotWanted(t *testing.T) {
	keep := policyTargeting(examplePolicy, "a-policy-0", "a")
	drop := policyTargeting(examplePolicy, "a-policy-1", "a")

	got := StalePolicies([]*unstructured.Unstructured{keep, drop}, []*unstructured.Unstructured{keep}, nil)

	assert.Equal(t, []*unstructured.Unstructured{drop}, got)
}

func TestStalePolicies_KeepsThePolicyOfARouteThatIsStillGoing(t *testing.T) {
	policy := policyTargeting(examplePolicy, "a-optimize-policy-0", "a-optimize")
	going := []client.Object{&gatewayv1.HTTPRoute{ObjectMeta: metav1.ObjectMeta{Name: "a-optimize"}}}

	assert.Empty(t, StalePolicies([]*unstructured.Unstructured{policy}, nil, going))
}

func TestStalePolicies_TheGoingRouteOfAnotherKindDoesNotKeepIt(t *testing.T) {
	policy := policyTargeting(examplePolicy, "a-policy-0", "a")
	going := []client.Object{&gatewayv1.GRPCRoute{ObjectMeta: metav1.ObjectMeta{Name: "a"}}}

	got := StalePolicies([]*unstructured.Unstructured{policy}, nil, going)

	assert.Equal(t, []*unstructured.Unstructured{policy}, got)
}

func TestStalePolicies_SameNameOfAnotherKindIsStaleOnceTheReplacementIsApplied(t *testing.T) {
	other := schema.GroupVersionKind{Group: "policies.example.com", Version: "v1alpha1", Kind: "OtherPolicy"}
	old := policyTargeting(examplePolicy, "a-policy-0", "a")
	replacement := policyTargeting(other, "a-policy-0", "a")

	got := StalePolicies(
		[]*unstructured.Unstructured{old, replacement.DeepCopy()},
		[]*unstructured.Unstructured{replacement},
		nil,
	)

	assert.Equal(t, []*unstructured.Unstructured{old}, got)
}

func TestStalePolicies_KeepsThePolicyOfARouteUntilItsReplacementIsApplied(t *testing.T) {
	other := schema.GroupVersionKind{Group: "policies.example.com", Version: "v1alpha1", Kind: "OtherPolicy"}
	old := policyTargeting(examplePolicy, "a-policy-0", "a")
	replacement := policyTargeting(other, "a-policy-0", "a")

	assert.Empty(t, StalePolicies([]*unstructured.Unstructured{old}, []*unstructured.Unstructured{replacement}, nil))
}

func TestStalePolicies_KeepsThePolicyOfARouteWhileAnotherPolicyHoldsAnOldSpec(t *testing.T) {
	kept := policyTargeting(examplePolicy, "a-policy-0", "a")
	require.NoError(t, unstructured.SetNestedField(kept.Object, "Log", "spec", "mode"))
	shifted := policyTargeting(examplePolicy, "a-policy-1", "a")
	desired := policyTargeting(examplePolicy, "a-policy-0", "a")
	require.NoError(t, unstructured.SetNestedField(desired.Object, "Deny", "spec", "mode"))

	got := StalePolicies([]*unstructured.Unstructured{kept, shifted}, []*unstructured.Unstructured{desired}, nil)

	assert.Empty(t, got)
}

func TestStalePolicies_PolicyOfARouteWithoutTemplatesIsStale(t *testing.T) {
	policy := policyTargeting(examplePolicy, "a-policy-0", "a")

	got := StalePolicies([]*unstructured.Unstructured{policy}, nil, nil)

	assert.Equal(t, []*unstructured.Unstructured{policy}, got)
}

func TestStalePolicies_KeepsThePolicyOfARouteWhileAnotherPolicyHoldsADroppedField(t *testing.T) {
	desired := policyTargeting(examplePolicy, "a-policy-0", "a")
	desired.SetAnnotations(map[string]string{AnnotationSpecHash: "hash-of-the-new-spec"})
	kept := desired.DeepCopy()
	require.NoError(t, unstructured.SetNestedField(kept.Object, "Deny", "spec", "mode"))
	kept.SetAnnotations(map[string]string{AnnotationSpecHash: "hash-of-the-old-spec"})
	shifted := policyTargeting(examplePolicy, "a-policy-1", "a")

	got := StalePolicies([]*unstructured.Unstructured{kept, shifted}, []*unstructured.Unstructured{desired}, nil)

	assert.Empty(t, got)
}
