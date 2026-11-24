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
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
)

var examplePolicy = schema.GroupVersionKind{Group: "policies.example.com", Version: "v1alpha1", Kind: "ExamplePolicy"}

const allowlistTemplate = `[{"apiVersion":"policies.example.com/v1alpha1","kind":"ExamplePolicy",` +
	`"spec":{"allowed":"{allowedCIDRs}","mode":"Deny"}}]`

// templateParams returns validParams with the parameter policyTemplates set
// to the JSON templates and the parameters of extra, decoded as pluginkit
// decodes a request.
func templateParams(t *testing.T, templates string, extra map[string]any) Params {
	t.Helper()
	raw, err := json.Marshal(validParams())
	require.NoError(t, err)

	var values map[string]any
	require.NoError(t, json.Unmarshal(raw, &values))
	values["policyTemplates"] = json.RawMessage(templates)
	for k, v := range extra {
		values[k] = v
	}

	raw, err = json.Marshal(values)
	require.NoError(t, err)
	var p Params
	require.NoError(t, json.Unmarshal(raw, &p))
	return p
}

// specOf returns spec of the first policy of templates for the cluster
// routes, with allowedCIDRs as the parameter of that name.
func specOf(t *testing.T, templates string, allowedCIDRs any) map[string]any {
	t.Helper()
	p := templateParams(t, templates, map[string]any{"allowedCIDRs": allowedCIDRs})
	got, refused := Policies(
		p,
		ClusterRoutes("orders-access", p, testHosts(t), standaloneEndpoints()),
		PolicyKinds{examplePolicy: true},
	)
	require.Nil(t, refused)
	require.NotEmpty(t, got)
	spec, _, err := unstructured.NestedMap(got[0].Object, "spec")
	require.NoError(t, err)
	return spec
}

// refusalOf returns the refusal of templates, with extra as parameters.
func refusalOf(t *testing.T, templates string, extra map[string]any, kinds PolicyKinds) string {
	t.Helper()
	p := templateParams(t, templates, extra)
	got, refused := Policies(p, ClusterRoutes("orders-access", p, testHosts(t), standaloneEndpoints()), kinds)
	assert.Nil(t, got)
	require.NotNil(t, refused)
	return refused.Reason + ": " + refused.Message
}

func TestPolicies_OnePolicyForEachTemplateAndRoute(t *testing.T) {
	p := templateParams(t, allowlistTemplate, map[string]any{"allowedCIDRs": []any{"10.0.0.0/8"}})
	routes := ClusterRoutes("orders-access", p, testHosts(t), standaloneEndpoints())

	got, refused := Policies(p, routes, PolicyKinds{examplePolicy: true})

	require.Nil(t, refused)
	require.Len(t, got, 2)
	assert.Equal(t, "orders-access-policy-0", got[0].GetName())
	assert.Equal(t, examplePolicy, got[0].GroupVersionKind())
	assert.Empty(t, got[0].GetNamespace(), "pluginkit puts it in the namespace of the request")
	assert.Empty(t, got[0].GetOwnerReferences(), "pluginkit sets the owner")
	spec, _, _ := unstructured.NestedMap(got[0].Object, "spec")
	assert.Equal(t, map[string]any{
		"allowed": []any{"10.0.0.0/8"},
		"mode":    "Deny",
		"targetRefs": []any{map[string]any{
			"group": "gateway.networking.k8s.io", "kind": "HTTPRoute", "name": "orders-access",
		}},
	}, spec)
	assert.Equal(t, "orders-access-grpc-policy-0", got[1].GetName())
	targets, _, _ := unstructured.NestedSlice(got[1].Object, "spec", "targetRefs")
	assert.Equal(t, []any{map[string]any{
		"group": "gateway.networking.k8s.io", "kind": "GRPCRoute", "name": "orders-access-grpc",
	}}, targets)
}

func TestPolicies_PoliciesShareNoValue(t *testing.T) {
	p := templateParams(t, allowlistTemplate, map[string]any{"allowedCIDRs": []any{"10.0.0.0/8"}})
	got, refused := Policies(
		p,
		ClusterRoutes("orders-access", p, testHosts(t), standaloneEndpoints()),
		PolicyKinds{examplePolicy: true},
	)
	require.Nil(t, refused)
	require.Len(t, got, 2)

	allowed, _, _ := unstructured.NestedSlice(got[0].Object, "spec", "allowed")
	allowed[0] = "changed"
	require.NoError(t, unstructured.SetNestedSlice(got[0].Object, allowed, "spec", "allowed"))

	other, _, _ := unstructured.NestedSlice(got[1].Object, "spec", "allowed")
	assert.Equal(t, []any{"10.0.0.0/8"}, other)
}

func TestPolicies_NoTemplatesGiveNoPolicies(t *testing.T) {
	p := validParams()
	got, refused := Policies(
		p,
		ClusterRoutes("orders-access", p, testHosts(t), standaloneEndpoints()),
		PolicyKinds{examplePolicy: true},
	)
	assert.Nil(t, refused)
	assert.Nil(t, got)
}

func TestPolicies_ReferenceInAListIsOneItem(t *testing.T) {
	spec := specOf(t,
		`[{"apiVersion":"policies.example.com/v1alpha1","kind":"ExamplePolicy","spec":{"rules":["{allowedCIDRs}"]}}]`,
		[]any{"a", "b"},
	)
	assert.Equal(t, []any{[]any{"a", "b"}}, spec["rules"])
}

func TestPolicies_ReferenceInANestedObjectIsResolved(t *testing.T) {
	spec := specOf(t,
		`[{"apiVersion":"policies.example.com/v1alpha1","kind":"ExamplePolicy",`+
			`"spec":{"authorization":{"rules":[{"principal":{"clientCIDRs":"{allowedCIDRs}"}}]}}}]`,
		[]any{"10.0.0.0/8"},
	)
	assert.Equal(t, map[string]any{
		"rules": []any{map[string]any{"principal": map[string]any{"clientCIDRs": []any{"10.0.0.0/8"}}}},
	}, spec["authorization"])
}

func TestPolicies_PartOfAStringIsNotAReference(t *testing.T) {
	spec := specOf(t,
		`[{"apiVersion":"policies.example.com/v1alpha1","kind":"ExamplePolicy","spec":{"zone":"zone-{allowedCIDRs}"}}]`,
		[]any{"a"},
	)
	assert.Equal(t, "zone-{allowedCIDRs}", spec["zone"])
}

func TestPolicies_ReferenceKeepsAnObjectValue(t *testing.T) {
	spec := specOf(t, allowlistTemplate, map[string]any{"cidr": "10.0.0.0/8", "limit": 3})
	assert.Equal(t, map[string]any{"cidr": "10.0.0.0/8", "limit": int64(3)}, spec["allowed"])
}

func TestPolicies_RefusesAnUnknownReference(t *testing.T) {
	got := refusalOf(t, allowlistTemplate, nil, PolicyKinds{examplePolicy: true})
	assert.Contains(t, got, pluginsv1alpha1.ReasonInvalidParameters+": ")
	assert.Contains(t, got, "policyTemplates[0].spec.allowed")
	assert.Contains(t, got, "{allowedCIDRs}")
}

func TestPolicies_RefusesAReferenceToANullParameter(t *testing.T) {
	got := refusalOf(t, allowlistTemplate, map[string]any{"allowedCIDRs": nil}, PolicyKinds{examplePolicy: true})
	assert.Contains(t, got, pluginsv1alpha1.ReasonInvalidParameters+": ")
	assert.Contains(t, got, "allowedCIDRs")
}

func TestPolicies_RefusesAReferenceToTheTemplates(t *testing.T) {
	for _, name := range []string{"policyTemplates", "routeFilters"} {
		templates := `[{"apiVersion":"policies.example.com/v1alpha1","kind":"ExamplePolicy","spec":{"x":"{` + name + `}"}}]`
		got := refusalOf(t, templates, map[string]any{"routeFilters": []any{}}, PolicyKinds{examplePolicy: true})
		assert.Contains(t, got, pluginsv1alpha1.ReasonInvalidParameters+": ", name)
		assert.Contains(t, got, "{"+name+"}", name)
	}
}

func TestPolicies_RefusesTargetRefsInTheTemplate(t *testing.T) {
	templates := `[{"apiVersion":"policies.example.com/v1alpha1","kind":"ExamplePolicy","spec":{"targetRefs":[]}}]`
	got := refusalOf(t, templates, nil, PolicyKinds{examplePolicy: true})
	assert.Contains(t, got, pluginsv1alpha1.ReasonInvalidParameters+": ")
	assert.Contains(t, got, "spec.targetRefs")
}

func TestPolicies_RefusesASpecThatIsNotAnObject(t *testing.T) {
	templates := `[{"apiVersion":"policies.example.com/v1alpha1","kind":"ExamplePolicy","spec":"deny"}]`
	got := refusalOf(t, templates, nil, PolicyKinds{examplePolicy: true})
	assert.Contains(t, got, pluginsv1alpha1.ReasonInvalidParameters+": ")
	assert.Contains(t, got, "policyTemplates[0].spec")
}

func TestPolicies_RefusesAKindThatIsNotListed(t *testing.T) {
	got := refusalOf(t, allowlistTemplate, map[string]any{"allowedCIDRs": []any{"a"}}, PolicyKinds{})
	assert.Contains(t, got, ReasonPolicyKindNotAllowed+": ")
	assert.Contains(t, got, "ExamplePolicy")
	assert.Contains(t, got, "policyKinds")
}

func TestPolicies_RefusesAListedKindThatWasNotServed(t *testing.T) {
	got := refusalOf(
		t,
		allowlistTemplate,
		map[string]any{"allowedCIDRs": []any{"a"}},
		PolicyKinds{examplePolicy: false},
	)
	assert.Contains(t, got, ReasonPolicyKindNotInstalled+": ")
	assert.Contains(t, got, "restart")
}

func TestPolicies_RefusesABadTemplateAlsoWithoutRoutes(t *testing.T) {
	p := templateParams(t, allowlistTemplate, map[string]any{"allowedCIDRs": []any{"a"}})

	got, refused := Policies(p, nil, PolicyKinds{})

	assert.Nil(t, got)
	require.NotNil(t, refused)
	assert.Equal(t, ReasonPolicyKindNotAllowed, refused.Reason)
}

func TestPolicies_SecondTemplateGetsItsOwnIndex(t *testing.T) {
	templates := `[{"apiVersion":"policies.example.com/v1alpha1","kind":"ExamplePolicy","spec":{"mode":"Deny"}},` +
		`{"apiVersion":"policies.example.com/v1alpha1","kind":"ExamplePolicy","spec":{"mode":"Log"}}]`
	p := templateParams(t, templates, nil)
	route := ManagementRoute("orders-access", p, testHosts(t), planeEndpoints())

	got, refused := Policies(p, []client.Object{route}, PolicyKinds{examplePolicy: true})

	require.Nil(t, refused)
	require.Len(t, got, 2)
	assert.Equal(t, "orders-access-policy-0", got[0].GetName())
	assert.Equal(t, "orders-access-policy-1", got[1].GetName())
	mode, _, _ := unstructured.NestedString(got[1].Object, "spec", "mode")
	assert.Equal(t, "Log", mode)
}
