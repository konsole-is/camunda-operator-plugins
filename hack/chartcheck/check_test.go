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
      containers: [{name: controller, image: example, args: [-leader-elect]}]
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
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata: {name: example-leader-election, namespace: plugins}
rules:
  - apiGroups: [coordination.k8s.io]
    resources: [leases]
    verbs: [get, list, watch, create, update, patch, delete]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata: {name: example-leader-election, namespace: plugins}
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: Role, name: example-leader-election}
subjects:
  - {kind: ServiceAccount, name: example, namespace: plugins}
`

// noLeaderElection is the problem of goodRender when its controller does not
// elect a leader.
const noLeaderElection = `the container controller of Deployment "example" does not pass -leader-elect, ` +
	`so a rolling update runs two writers`

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

func TestProblemsRefusesAnAggregatedClusterRole(t *testing.T) {
	aggregated := `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata: {name: example-extra}
aggregationRule:
  clusterRoleSelectors: [{}]
rules: []`
	charts := map[string][]string{
		"an extra role": {goodRender, aggregated},
		"the serve role": {
			strings.Replace(goodRender, "rules:\n", "aggregationRule:\n  clusterRoleSelectors: [{}]\nrules:\n", 1),
		},
	}
	want := map[string]string{
		"an extra role":  `ClusterRole "example-extra" aggregates other roles, so the chart cannot show what it grants`,
		"the serve role": `ClusterRole "example-serve" aggregates other roles, so the chart cannot show what it grants`,
	}

	for name, docs := range charts {
		t.Run(name, func(t *testing.T) {
			assert.Contains(t, problems(render(t, docs...), []string{"a", "b"}, "plugins"), want[name])
		})
	}
}

func TestProblemsRefusesARoleBindingOfARoleThatTheChartDoesNotRender(t *testing.T) {
	clusterAdmin := `apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata: {name: example-admin, namespace: plugins}
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: cluster-admin}
subjects:
  - {kind: ServiceAccount, name: example, namespace: plugins}`

	got := problems(render(t, goodRender, clusterAdmin), []string{"a", "b"}, "plugins")

	assert.Equal(
		t,
		[]string{
			`RoleBinding "example-admin" binds the ClusterRole "cluster-admin", which the chart does not render`,
		},
		got,
	)
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

func TestProblemsRefusesAControllerWithoutLeaderElection(t *testing.T) {
	objects := render(t, strings.Replace(goodRender, "args: [-leader-elect]", "args: []", 1))

	assert.Equal(t, []string{
		noLeaderElection,
	}, problems(objects, []string{"a", "b"}, "plugins"))
}

func TestProblemsAcceptsTheLeaderElectionFlagWithAValue(t *testing.T) {
	for _, flag := range []string{"--leader-elect", "-leader-elect=true", "--leader-elect=true"} {
		objects := render(t, strings.Replace(goodRender, "args: [-leader-elect]", "args: ["+flag+"]", 1))

		assert.Empty(t, problems(objects, []string{"a", "b"}, "plugins"), flag)
	}
}

func TestProblemsReadsTheLeaderElectionFlagAsTheControllerDoes(t *testing.T) {
	cases := map[string]string{
		"a positional argument":              "args: [leader-elect]",
		"a later false":                      "args: [-leader-elect, -leader-elect=false]",
		"after the terminator":               "args: [--, -leader-elect]",
		"after a positional":                 "args: [run, -leader-elect]",
		"a later false after a spaced value": "args: [-leader-elect, -leader-election-id, example, -leader-elect=false]",
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			objects := render(t, strings.Replace(goodRender, "args: [-leader-elect]", args, 1))

			assert.Contains(
				t,
				problems(objects, []string{"a", "b"}, "plugins"),
				noLeaderElection,
			)
		})
	}
}

func TestProblemsReadsAStringFlagWithItsValueInTheNextArgument(t *testing.T) {
	for _, args := range []string{
		"args: [-registrations, /etc/plugin/registrations.yaml, -leader-elect]",
		"args: [-leader-elect, -zap-log-level, debug]",
	} {
		objects := render(t, strings.Replace(goodRender, "args: [-leader-elect]", args, 1))

		assert.Empty(t, problems(objects, []string{"a", "b"}, "plugins"), args)
	}

	named := strings.Replace(
		goodRender,
		"args: [-leader-elect]",
		"args: [-leader-elect, -leader-election-id, example]",
		1,
	)
	named = strings.Replace(named, "verbs: [get, list, watch, create, update, patch, delete]", `verbs: [create]
  - apiGroups: [coordination.k8s.io]
    resources: [leases]
    verbs: [get, update]
    resourceNames: [example]`, 1)
	assert.Empty(t, problems(render(t, named), []string{"a", "b"}, "plugins"))
}

func TestProblemsReadsTheFlagsOfTheControllerContainerOnly(t *testing.T) {
	sidecar := strings.Replace(goodRender,
		"containers: [{name: controller, image: example, args: [-leader-elect]}]",
		"containers: [{name: controller, image: example, args: [-leader-elect=false]}, "+
			"{name: sidecar, image: example, args: [-leader-elect]}]", 1)

	assert.Contains(
		t,
		problems(render(t, sidecar), []string{"a", "b"}, "plugins"),
		noLeaderElection,
	)

	lease := strings.Replace(goodRender,
		"containers: [{name: controller, image: example, args: [-leader-elect]}]",
		"containers: [{name: controller, image: example, args: [-leader-elect, -leader-election-id=other]}, "+
			"{name: sidecar, image: example, args: [-leader-election-id=example]}]", 1)
	lease = strings.Replace(lease, "verbs: [get, list, watch, create, update, patch, delete]", `verbs: [create]
  - apiGroups: [coordination.k8s.io]
    resources: [leases]
    verbs: [get, update]
    resourceNames: [example]`, 1)

	assert.Contains(
		t,
		problems(render(t, lease), []string{"a", "b"}, "plugins"),
		`no Role bound to the controller ServiceAccount plugins/example in plugins grants the leases of leader election`,
	)
}

func TestProblemsAcceptsLeasePermissionsSplitAcrossRules(t *testing.T) {
	split := strings.Replace(goodRender, `  - apiGroups: [coordination.k8s.io]
    resources: [leases]
    verbs: [get, list, watch, create, update, patch, delete]`, `  - apiGroups: [coordination.k8s.io]
    resources: [leases]
    verbs: [create]
  - apiGroups: [coordination.k8s.io]
    resources: [leases]
    verbs: [get, update]
    resourceNames: [example]`, 1)
	split = strings.Replace(split, "args: [-leader-elect]", "args: [-leader-elect, -leader-election-id=example]", 1)

	assert.Empty(t, problems(render(t, split), []string{"a", "b"}, "plugins"))
}

func TestProblemsRefusesLeasePermissionsThatCannotTakeTheLease(t *testing.T) {
	rule := `  - apiGroups: [coordination.k8s.io]
    resources: [leases]
    verbs: [get, list, watch, create, update, patch, delete]`
	cases := map[string]string{
		"a create with a name": rule + "\n    resourceNames: [example]",
		"an update of another name": `  - apiGroups: [coordination.k8s.io]
    resources: [leases]
    verbs: [create]
  - apiGroups: [coordination.k8s.io]
    resources: [leases]
    verbs: [get, update]
    resourceNames: [other]`,
	}
	for name, rules := range cases {
		t.Run(name, func(t *testing.T) {
			chart := strings.Replace(goodRender, rule, rules, 1)
			chart = strings.Replace(
				chart,
				"args: [-leader-elect]",
				"args: [-leader-elect, -leader-election-id=example]",
				1,
			)

			assert.Contains(
				t,
				problems(render(t, chart), []string{"a", "b"}, "plugins"),
				`no Role bound to the controller ServiceAccount plugins/example in plugins grants the leases of leader election`,
			)
		})
	}
}

func TestProblemsRefusesLeaderElectionWithoutALeaseRole(t *testing.T) {
	cases := map[string]string{
		"no lease rule": strings.Replace(goodRender, "resources: [leases]", "resources: [configmaps]", 1),
		"a role in another namespace": strings.Replace(goodRender,
			"kind: Role\nmetadata: {name: example-leader-election, namespace: plugins}",
			"kind: Role\nmetadata: {name: example-leader-election, namespace: other}", 1),
		"a binding to another account": strings.Replace(goodRender,
			"roleRef: {apiGroup: rbac.authorization.k8s.io, kind: Role, name: example-leader-election}\n"+
				"subjects:\n  - {kind: ServiceAccount, name: example, namespace: plugins}",
			"roleRef: {apiGroup: rbac.authorization.k8s.io, kind: Role, name: example-leader-election}\n"+
				"subjects:\n  - {kind: ServiceAccount, name: other, namespace: plugins}", 1),
	}
	for name, chart := range cases {
		t.Run(name, func(t *testing.T) {
			require.NotEqual(t, goodRender, chart)

			assert.Contains(
				t,
				problems(render(t, chart), []string{"a", "b"}, "plugins"),
				`no Role bound to the controller ServiceAccount plugins/example in plugins grants the leases of leader election`,
			)
		})
	}
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
