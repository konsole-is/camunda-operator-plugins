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

package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// goodRender is what a plugin chart with the registrations a and b renders in
// the namespace plugins.
const goodRender = `
apiVersion: v1
kind: ServiceAccount
metadata: {name: example, namespace: plugins}
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: example, namespace: plugins}
spec:
  template:
    spec:
      serviceAccountName: example
      containers: [{name: controller, image: example}]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata: {name: example-serve}
rules:
  - apiGroups: [plugins.camunda.io]
    resources: [pluginregistrations]
    verbs: [serve]
    resourceNames: [a, b]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata: {name: example-serve}
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: example-serve}
subjects:
  - {kind: ServiceAccount, name: example, namespace: plugins}
`

func render(t *testing.T, docs ...string) []*unstructured.Unstructured {
	t.Helper()
	objects, err := readObjects(strings.NewReader(strings.Join(docs, "\n---\n")))
	require.NoError(t, err)

	return objects
}

func TestProblemsAcceptsAChartThatServesItsOwnRegistrations(t *testing.T) {
	assert.Empty(t, problems(render(t, goodRender), []string{"a", "b"}, "plugins"))
}

func TestProblemsRefusesAnAdmissionPolicyOrBinding(t *testing.T) {
	for _, kind := range []string{"ValidatingAdmissionPolicy", "ValidatingAdmissionPolicyBinding"} {
		objects := render(
			t,
			goodRender,
			"apiVersion: admissionregistration.k8s.io/v1\nkind: "+kind+"\nmetadata: {name: grants}",
		)

		assert.Contains(t, problems(objects, []string{"a", "b"}, "plugins"),
			"the chart renders "+kind+` "grants": the operator chart holds the one grant admission policy`)
	}
}

func TestProblemsRefusesServeOnEveryRegistration(t *testing.T) {
	objects := render(t, strings.Replace(goodRender, "resourceNames: [a, b]", "", 1))

	assert.Contains(
		t,
		problems(objects, []string{"a", "b"}, "plugins"),
		`ClusterRole "example-serve" grants serve on every registration: set resourceNames to the registrations of the chart`,
	)
}

func TestProblemsRefusesAWildcardVerb(t *testing.T) {
	objects := render(t, strings.Replace(
		strings.Replace(goodRender, "verbs: [serve]", `verbs: ["*"]`, 1), "resourceNames: [a, b]", "", 1))

	assert.Contains(
		t,
		problems(objects, []string{"a", "b"}, "plugins"),
		`ClusterRole "example-serve" grants serve on every registration: set resourceNames to the registrations of the chart`,
	)
}

func TestProblemsRefusesServeOnARegistrationOfAnotherChart(t *testing.T) {
	objects := render(t, strings.Replace(goodRender, "resourceNames: [a, b]", "resourceNames: [a, b, c]", 1))

	assert.Contains(t, problems(objects, []string{"a", "b"}, "plugins"),
		`ClusterRole "example-serve" grants serve on "c", which is not a registration of the chart`)
}

func TestProblemsRefusesARegistrationWithoutServe(t *testing.T) {
	objects := render(t, strings.Replace(goodRender, "resourceNames: [a, b]", "resourceNames: [a]", 1))

	assert.Contains(t, problems(objects, []string{"a", "b"}, "plugins"),
		`no ClusterRole bound to the controller ServiceAccount plugins/example grants serve on the registration "b"`)
}

func TestProblemsRefusesARegistrationThatOnlyAnUnboundRoleServes(t *testing.T) {
	unbound := `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata: {name: example-serve-b}
rules:
  - apiGroups: [plugins.camunda.io]
    resources: [pluginregistrations]
    verbs: [serve]
    resourceNames: [b]`
	objects := render(t, strings.Replace(goodRender, "resourceNames: [a, b]", "resourceNames: [a]", 1), unbound)

	assert.Contains(t, problems(objects, []string{"a", "b"}, "plugins"),
		`no ClusterRole bound to the controller ServiceAccount plugins/example grants serve on the registration "b"`)
}

func TestProblemsRefusesABindingToAnotherServiceAccount(t *testing.T) {
	objects := render(t, strings.Replace(goodRender,
		"{kind: ServiceAccount, name: example, namespace: plugins}",
		"{kind: ServiceAccount, name: intruder, namespace: plugins}", 1))

	got := problems(objects, []string{"a", "b"}, "plugins")

	assert.Contains(t, got, `ClusterRoleBinding "example-serve" binds serve to ServiceAccount plugins/intruder, `+
		`which runs no controller of the chart`)
	assert.Contains(t, got, `no ClusterRoleBinding binds serve to the controller ServiceAccount plugins/example`)
}

func TestProblemsRefusesAControllerWithoutItsServiceAccount(t *testing.T) {
	objects := render(
		t,
		strings.Replace(goodRender, "kind: ServiceAccount\nmetadata: {name: example, namespace: plugins}",
			"kind: ConfigMap\nmetadata: {name: example, namespace: plugins}", 1),
	)

	assert.Contains(t, problems(objects, []string{"a", "b"}, "plugins"),
		`Deployment "example" runs as the ServiceAccount "plugins/example", which the chart does not render`)
}

func TestProblemsRefusesAServiceAccountInAnotherNamespace(t *testing.T) {
	objects := render(
		t,
		strings.Replace(goodRender, "kind: ServiceAccount\nmetadata: {name: example, namespace: plugins}",
			"kind: ServiceAccount\nmetadata: {name: example, namespace: other}", 1),
	)

	assert.Contains(t, problems(objects, []string{"a", "b"}, "plugins"),
		`Deployment "example" runs as the ServiceAccount "plugins/example", which the chart does not render`)
}

func TestProblemsPlacesObjectsWithoutANamespaceInTheReleaseNamespace(t *testing.T) {
	objects := render(t, strings.ReplaceAll(goodRender, "{name: example, namespace: plugins}", "{name: example}"))

	assert.Empty(t, problems(objects, []string{"a", "b"}, "plugins"))
}

func TestProblemsRefusesABindingOfARoleThatTheChartDoesNotRender(t *testing.T) {
	clusterAdmin := `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata: {name: example-admin}
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: cluster-admin}
subjects:
  - {kind: ServiceAccount, name: example, namespace: plugins}`

	assert.Contains(t, problems(render(t, goodRender, clusterAdmin), []string{"a", "b"}, "plugins"),
		`ClusterRoleBinding "example-admin" binds the ClusterRole "cluster-admin", which the chart does not render`)
}

func TestProblemsChecksTheItemsOfAList(t *testing.T) {
	list := `apiVersion: v1
kind: List
items:
  - apiVersion: admissionregistration.k8s.io/v1
    kind: ValidatingAdmissionPolicyBinding
    metadata: {name: grants}`

	assert.Contains(
		t,
		problems(render(t, goodRender, list), []string{"a", "b"}, "plugins"),
		`the chart renders ValidatingAdmissionPolicyBinding "grants": `+
			`the operator chart holds the one grant admission policy`,
	)
}

func TestProblemsRefusesAChartWithoutAController(t *testing.T) {
	assert.Contains(t, problems(nil, []string{"a"}, "plugins"), "the chart renders no Deployment")
}

func TestProblemsRefusesValuesWithoutARegistration(t *testing.T) {
	assert.Equal(t, []string{"the values name no registration"}, problems(render(t, goodRender), nil, "plugins"))
}

func TestReadRegistrationsReadsTheNamesOfTheValues(t *testing.T) {
	names, err := readRegistrations("testdata/two-registrations.yaml")

	require.NoError(t, err)
	assert.Equal(t, []string{"skeleton-public", "skeleton-internal"}, names)
}
