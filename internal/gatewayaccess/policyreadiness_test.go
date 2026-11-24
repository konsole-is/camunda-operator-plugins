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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// desiredPolicies returns the policies of allowlistTemplate for the two
// cluster routes: orders-access-policy-0 and orders-access-grpc-policy-0.
func desiredPolicies(t *testing.T) []*unstructured.Unstructured {
	t.Helper()
	p := templateParams(t, allowlistTemplate, map[string]any{"allowedCIDRs": []any{"10.0.0.0/8"}})
	got, refused := Policies(p, clusterRoutes(t, p), PolicyKinds{examplePolicy: true})
	require.Nil(t, refused)
	require.Len(t, got, 2)
	return got
}

// livePolicy returns a copy of desired in team-a as the API server stores it,
// with the generation and status.ancestors set to ancestors.
func livePolicy(
	t *testing.T,
	desired *unstructured.Unstructured,
	generation int64,
	ancestors ...gatewayv1.PolicyAncestorStatus,
) *unstructured.Unstructured {
	t.Helper()
	live := desired.DeepCopy()
	live.SetNamespace("team-a")
	live.SetGeneration(generation)

	status, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&gatewayv1.PolicyStatus{Ancestors: ancestors})
	require.NoError(t, err)
	live.Object["status"] = status
	return live
}

// ancestor is a status.ancestors entry of controller for the Gateway
// gateway-system/public, with an Accepted condition.
func ancestor(
	controller gatewayv1.GatewayController,
	status metav1.ConditionStatus,
	reason string,
	generation int64,
) gatewayv1.PolicyAncestorStatus {
	return gatewayv1.PolicyAncestorStatus{
		AncestorRef:    ParentRef(validParams()),
		ControllerName: controller,
		Conditions: []metav1.Condition{{
			Type:               string(gatewayv1.PolicyConditionAccepted),
			Status:             status,
			Reason:             reason,
			ObservedGeneration: generation,
		}},
	}
}

func livePolicies(objs ...*unstructured.Unstructured) map[PolicyKey]*unstructured.Unstructured {
	live := map[PolicyKey]*unstructured.Unstructured{}
	for _, obj := range objs {
		live[KeyOfPolicy(obj)] = obj
	}
	return live
}

func TestPolicyWaiting_AcceptedByTheGatewayAncestorCounts(t *testing.T) {
	desired := desiredPolicies(t)
	live := livePolicies(
		livePolicy(t, desired[0], 1, ancestor(testController, metav1.ConditionTrue, "Accepted", 1)),
		livePolicy(t, desired[1], 3, ancestor(testController, metav1.ConditionTrue, "Accepted", 3)),
	)

	accepted, waiting := PolicyWaiting(desired, live, ParentRef(validParams()), testController)

	assert.Equal(t, 2, accepted)
	assert.Nil(t, waiting)
}

func TestPolicyWaiting_AcceptedByTheRouteAncestorCounts(t *testing.T) {
	desired := desiredPolicies(t)
	byRoute := ancestor(testController, metav1.ConditionTrue, "Accepted", 1)
	byRoute.AncestorRef = gatewayv1.ParentReference{Kind: new(gatewayv1.Kind("HTTPRoute")), Name: "orders-access"}
	live := livePolicies(
		livePolicy(t, desired[0], 1, byRoute),
		livePolicy(t, desired[1], 1, ancestor(testController, metav1.ConditionTrue, "Accepted", 1)),
	)

	accepted, waiting := PolicyWaiting(desired, live, ParentRef(validParams()), testController)

	assert.Equal(t, 2, accepted)
	assert.Nil(t, waiting)
}

func TestPolicyWaiting_RejectedPolicyGivesTheReasonOfTheController(t *testing.T) {
	desired := desiredPolicies(t)
	rejected := ancestor(testController, metav1.ConditionFalse, "Invalid", 1)
	rejected.Conditions[0].Message = "the CIDR 10.0.0.0/33 is not valid"
	live := livePolicies(
		livePolicy(t, desired[0], 1, rejected),
		livePolicy(t, desired[1], 1, ancestor(testController, metav1.ConditionTrue, "Accepted", 1)),
	)

	accepted, waiting := PolicyWaiting(desired, live, ParentRef(validParams()), testController)

	assert.Equal(t, 1, accepted)
	require.NotNil(t, waiting)
	assert.Equal(t, "Invalid", waiting.Reason)
	assert.Equal(t, "ExamplePolicy orders-access-policy-0: the CIDR 10.0.0.0/33 is not valid", waiting.Message)
}

// onlyFirstWaiting returns the waiting of desired when only desired[0] has
// the live copy first and desired[1] is accepted.
func onlyFirstWaiting(
	t *testing.T,
	desired []*unstructured.Unstructured,
	first *unstructured.Unstructured,
) (int, string, string) {
	t.Helper()
	live := livePolicies(livePolicy(t, desired[1], 1, ancestor(testController, metav1.ConditionTrue, "Accepted", 1)))
	if first != nil {
		live[KeyOfPolicy(first)] = first
	}

	accepted, waiting := PolicyWaiting(desired, live, ParentRef(validParams()), testController)
	require.NotNil(t, waiting)
	return accepted, waiting.Reason, waiting.Message
}

func TestPolicyWaiting_MissingPolicyIsPending(t *testing.T) {
	accepted, reason, message := onlyFirstWaiting(t, desiredPolicies(t), nil)
	assert.Equal(t, 1, accepted)
	assert.Equal(t, "Pending", reason)
	assert.Equal(
		t,
		"ExamplePolicy orders-access-policy-0 waits for the controller example.com/gateway to accept it",
		message,
	)
}

func TestPolicyWaiting_PolicyWithoutAReportIsPending(t *testing.T) {
	desired := desiredPolicies(t)
	_, reason, message := onlyFirstWaiting(t, desired, livePolicy(t, desired[0], 1))
	assert.Equal(t, "Pending", reason)
	assert.Contains(t, message, "orders-access-policy-0")
}

func TestPolicyWaiting_StatusOfAnOlderGenerationIsPending(t *testing.T) {
	desired := desiredPolicies(t)
	old := livePolicy(t, desired[0], 2, ancestor(testController, metav1.ConditionTrue, "Accepted", 1))
	_, reason, _ := onlyFirstWaiting(t, desired, old)
	assert.Equal(t, "Pending", reason)
}

func TestPolicyWaiting_RejectionOfAnOlderGenerationIsPending(t *testing.T) {
	desired := desiredPolicies(t)
	old := livePolicy(t, desired[0], 2, ancestor(testController, metav1.ConditionFalse, "Invalid", 1))
	_, reason, _ := onlyFirstWaiting(t, desired, old)
	assert.Equal(t, "Pending", reason)
}

func TestPolicyWaiting_EntryOfAnotherControllerDoesNotCount(t *testing.T) {
	desired := desiredPolicies(t)
	other := livePolicy(t, desired[0], 1, ancestor("other.example/controller", metav1.ConditionTrue, "Accepted", 1))
	_, reason, _ := onlyFirstWaiting(t, desired, other)
	assert.Equal(t, "Pending", reason)
}

func TestPolicyWaiting_RejectionOfAnotherControllerDoesNotCount(t *testing.T) {
	desired := desiredPolicies(t)
	live := livePolicies(
		livePolicy(t, desired[0], 1,
			ancestor("other.example/controller", metav1.ConditionFalse, "Invalid", 1),
			ancestor(testController, metav1.ConditionTrue, "Accepted", 1),
		),
		livePolicy(t, desired[1], 1, ancestor(testController, metav1.ConditionTrue, "Accepted", 1)),
	)

	accepted, waiting := PolicyWaiting(desired, live, ParentRef(validParams()), testController)

	assert.Equal(t, 2, accepted)
	assert.Nil(t, waiting)
}

func TestPolicyWaiting_LiveCopyWithAnotherSpecIsPending(t *testing.T) {
	desired := desiredPolicies(t)
	changed := livePolicy(t, desired[0], 1, ancestor(testController, metav1.ConditionTrue, "Accepted", 1))
	require.NoError(t, unstructured.SetNestedField(changed.Object, "Allow", "spec", "mode"))

	_, reason, message := onlyFirstWaiting(t, desired, changed)

	assert.Equal(t, "Pending", reason)
	assert.Equal(t, "ExamplePolicy orders-access-policy-0 differs from the policy that the plugin applies", message)
}

func TestPolicyWaiting_LiveCopyWithAnotherListIsPending(t *testing.T) {
	desired := desiredPolicies(t)
	changed := livePolicy(t, desired[0], 1, ancestor(testController, metav1.ConditionTrue, "Accepted", 1))
	require.NoError(
		t,
		unstructured.SetNestedSlice(changed.Object, []any{"10.0.0.0/8", "192.168.0.0/16"}, "spec", "allowed"),
	)

	_, reason, _ := onlyFirstWaiting(t, desired, changed)

	assert.Equal(t, "Pending", reason)
}

func TestPolicyWaiting_DefaultedFieldsOfTheLiveCopyDoNotCount(t *testing.T) {
	desired := desiredPolicies(t)
	defaulted := livePolicy(t, desired[0], 1, ancestor(testController, metav1.ConditionTrue, "Accepted", 1))
	require.NoError(t, unstructured.SetNestedField(defaulted.Object, int64(30), "spec", "timeout"))
	live := livePolicies(
		defaulted,
		livePolicy(t, desired[1], 1, ancestor(testController, metav1.ConditionTrue, "Accepted", 1)),
	)

	accepted, waiting := PolicyWaiting(desired, live, ParentRef(validParams()), testController)

	assert.Equal(t, 2, accepted)
	assert.Nil(t, waiting)
}

func TestPolicyWaiting_NumbersCompareByValue(t *testing.T) {
	p := templateParams(t,
		`[{"apiVersion":"policies.example.com/v1alpha1","kind":"ExamplePolicy","spec":{"limit":3}}]`, nil)
	desired, refused := Policies(p, clusterRoutes(t, p), PolicyKinds{examplePolicy: true})
	require.Nil(t, refused)
	live := livePolicies(
		livePolicy(t, desired[0], 1, ancestor(testController, metav1.ConditionTrue, "Accepted", 1)),
		livePolicy(t, desired[1], 1, ancestor(testController, metav1.ConditionTrue, "Accepted", 1)),
	)
	require.NoError(t, unstructured.SetNestedField(live[KeyOfPolicy(desired[0])].Object, float64(3), "spec", "limit"))

	accepted, waiting := PolicyWaiting(desired, live, ParentRef(validParams()), testController)

	assert.Equal(t, 2, accepted)
	assert.Nil(t, waiting)
}

func TestPolicyWaiting_TerminatingPolicyIsPending(t *testing.T) {
	desired := desiredPolicies(t)
	going := livePolicy(t, desired[0], 1, ancestor(testController, metav1.ConditionTrue, "Accepted", 1))
	going.SetDeletionTimestamp(new(metav1.Now()))

	_, reason, _ := onlyFirstWaiting(t, desired, going)

	assert.Equal(t, "Pending", reason)
}

func TestPolicyWaiting_NamesTheFirstPolicyByKindThenName(t *testing.T) {
	accepted, waiting := PolicyWaiting(desiredPolicies(t), nil, ParentRef(validParams()), testController)

	assert.Equal(t, 0, accepted)
	require.NotNil(t, waiting)
	assert.Contains(t, waiting.Message, "ExamplePolicy orders-access-grpc-policy-0 ")
}

func TestPolicyWaiting_AcceptanceForAnotherGatewayDoesNotCount(t *testing.T) {
	desired := desiredPolicies(t)
	elsewhere := ancestor(testController, metav1.ConditionTrue, "Accepted", 1)
	elsewhere.AncestorRef.Name = "internal"

	_, reason, _ := onlyFirstWaiting(t, desired, livePolicy(t, desired[0], 1, elsewhere))

	assert.Equal(t, "Pending", reason)
}

func TestPolicyWaiting_RejectionForAnotherGatewayDoesNotCount(t *testing.T) {
	desired := desiredPolicies(t)
	elsewhere := ancestor(testController, metav1.ConditionFalse, "Invalid", 1)
	elsewhere.AncestorRef.Name = "internal"
	live := livePolicies(
		livePolicy(t, desired[0], 1, elsewhere, ancestor(testController, metav1.ConditionTrue, "Accepted", 1)),
		livePolicy(t, desired[1], 1, ancestor(testController, metav1.ConditionTrue, "Accepted", 1)),
	)

	accepted, waiting := PolicyWaiting(desired, live, ParentRef(validParams()), testController)

	assert.Equal(t, 2, accepted)
	assert.Nil(t, waiting)
}

func TestPolicyWaiting_AcceptanceForAnotherRouteDoesNotCount(t *testing.T) {
	desired := desiredPolicies(t)
	byRoute := ancestor(testController, metav1.ConditionTrue, "Accepted", 1)
	byRoute.AncestorRef = gatewayv1.ParentReference{Kind: new(gatewayv1.Kind("HTTPRoute")), Name: "other-access"}

	_, reason, _ := onlyFirstWaiting(t, desired, livePolicy(t, desired[0], 1, byRoute))

	assert.Equal(t, "Pending", reason)
}

func TestPolicyWaiting_AcceptanceForAListenerOfTheGatewayCounts(t *testing.T) {
	desired := desiredPolicies(t)
	byListener := ancestor(testController, metav1.ConditionTrue, "Accepted", 1)
	byListener.AncestorRef.SectionName = new(gatewayv1.SectionName("https"))
	live := livePolicies(
		livePolicy(t, desired[0], 1, byListener),
		livePolicy(t, desired[1], 1, ancestor(testController, metav1.ConditionTrue, "Accepted", 1)),
	)

	accepted, waiting := PolicyWaiting(desired, live, ParentRef(validParams()), testController)

	assert.Equal(t, 2, accepted)
	assert.Nil(t, waiting)
}

// withOldSpec returns live as it stays when the template of desired dropped
// the field mode: the live copy still holds mode, and the hash of that spec.
func withOldSpec(t *testing.T, live *unstructured.Unstructured) *unstructured.Unstructured {
	t.Helper()
	old := live.DeepCopy()
	require.NoError(t, unstructured.SetNestedField(old.Object, "Deny", "spec", "mode"))
	old.SetAnnotations(map[string]string{AnnotationSpecHash: "hash-of-the-old-spec"})
	return old
}

func TestPolicyWaiting_LiveCopyWithAFieldThatTheTemplateDroppedIsPending(t *testing.T) {
	p := templateParams(t, `[{"apiVersion":"policies.example.com/v1alpha1","kind":"ExamplePolicy","spec":{}}]`, nil)
	desired, refused := Policies(p, clusterRoutes(t, p), PolicyKinds{examplePolicy: true})
	require.Nil(t, refused)
	old := withOldSpec(t, livePolicy(t, desired[0], 1, ancestor(testController, metav1.ConditionTrue, "Accepted", 1)))

	_, reason, message := onlyFirstWaiting(t, desired, old)

	assert.Equal(t, "Pending", reason)
	assert.Contains(t, message, "differs from the policy that the plugin applies")
}

func TestPolicies_SetTheHashOfTheirSpec(t *testing.T) {
	p := templateParams(t, allowlistTemplate, map[string]any{"allowedCIDRs": []any{"10.0.0.0/8"}})
	got, refused := Policies(p, clusterRoutes(t, p), PolicyKinds{examplePolicy: true})
	require.Nil(t, refused)

	other := templateParams(t, allowlistTemplate, map[string]any{"allowedCIDRs": []any{"192.168.0.0/16"}})
	changed, refused := Policies(other, clusterRoutes(t, other), PolicyKinds{examplePolicy: true})
	require.Nil(t, refused)

	assert.NotEmpty(t, got[0].GetAnnotations()[AnnotationSpecHash])
	assert.NotEqual(t, got[0].GetAnnotations()[AnnotationSpecHash], got[1].GetAnnotations()[AnnotationSpecHash])
	assert.NotEqual(t, got[0].GetAnnotations()[AnnotationSpecHash], changed[0].GetAnnotations()[AnnotationSpecHash])
	again, _ := Policies(p, clusterRoutes(t, p), PolicyKinds{examplePolicy: true})
	assert.Equal(t, got[0].GetAnnotations(), again[0].GetAnnotations())
}

func TestPolicyWaiting_LargeIntegersCompareExactly(t *testing.T) {
	p := templateParams(
		t,
		`[{"apiVersion":"policies.example.com/v1alpha1","kind":"ExamplePolicy","spec":{"limit":9007199254740992}}]`,
		nil,
	)
	desired, refused := Policies(p, clusterRoutes(t, p), PolicyKinds{examplePolicy: true})
	require.Nil(t, refused)
	changed := livePolicy(t, desired[0], 1, ancestor(testController, metav1.ConditionTrue, "Accepted", 1))
	require.NoError(t, unstructured.SetNestedField(changed.Object, int64(9007199254740993), "spec", "limit"))

	_, reason, _ := onlyFirstWaiting(t, desired, changed)

	assert.Equal(t, "Pending", reason)
}
