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
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

const (
	pluginsGroup          = "plugins.camunda.io"
	registrationsResource = "pluginregistrations"
	serveVerb             = "serve"
	clusterRoleKind       = "ClusterRole"
)

// roleRef is the role reference of one ClusterRoleBinding or RoleBinding.
type roleRef struct {
	kind string
	name string
	ref  rbacv1.RoleRef
}

// served is what one ClusterRole grants serve on: every registration, or the
// names of its rules.
type served struct {
	all   bool
	names []string
}

// problems returns each way in which the rendered objects of a plugin chart
// break the chart rule for the registrations of its values, in the release
// namespace.
func problems(objects []*unstructured.Unstructured, registrations []string, namespace string) []string {
	if len(registrations) == 0 {
		return []string{"the values name no registration"}
	}

	var found []string
	accounts := map[string]bool{}
	var deployments []appsv1.Deployment
	var roles []rbacv1.ClusterRole
	var bindings []rbacv1.ClusterRoleBinding
	var leaseRoles []rbacv1.Role
	var roleBindings []rbacv1.RoleBinding
	var refs []roleRef
	for _, o := range objects {
		var err error
		switch o.GetKind() {
		case "ValidatingAdmissionPolicy", "ValidatingAdmissionPolicyBinding":
			found = append(found, fmt.Sprintf(
				"the chart renders %s %q: the operator chart holds the one grant admission policy",
				o.GetKind(),
				o.GetName(),
			))
		case "ServiceAccount":
			accounts[account(inNamespace(o.GetNamespace(), namespace), o.GetName())] = true
		case "Deployment":
			var d appsv1.Deployment
			err = runtime.DefaultUnstructuredConverter.FromUnstructured(o.Object, &d)
			d.Namespace = inNamespace(d.Namespace, namespace)
			deployments = append(deployments, d)
		case clusterRoleKind:
			var r rbacv1.ClusterRole
			err = runtime.DefaultUnstructuredConverter.FromUnstructured(o.Object, &r)
			roles = append(roles, r)
			// The API server replaces the rules of an aggregated role with the
			// rules of the roles that it selects, also roles outside the chart.
			if r.AggregationRule != nil {
				found = append(found, fmt.Sprintf(
					"ClusterRole %q aggregates other roles, so the chart cannot show what it grants", r.Name))
			}
		case "ClusterRoleBinding":
			var b rbacv1.ClusterRoleBinding
			err = runtime.DefaultUnstructuredConverter.FromUnstructured(o.Object, &b)
			bindings = append(bindings, b)
			refs = append(refs, roleRef{kind: o.GetKind(), name: b.Name, ref: b.RoleRef})
		case "Role":
			var r rbacv1.Role
			err = runtime.DefaultUnstructuredConverter.FromUnstructured(o.Object, &r)
			r.Namespace = inNamespace(r.Namespace, namespace)
			leaseRoles = append(leaseRoles, r)
		case "RoleBinding":
			var b rbacv1.RoleBinding
			err = runtime.DefaultUnstructuredConverter.FromUnstructured(o.Object, &b)
			b.Namespace = inNamespace(b.Namespace, namespace)
			roleBindings = append(roleBindings, b)
			refs = append(refs, roleRef{kind: o.GetKind(), name: b.Name, ref: b.RoleRef})
		}
		if err != nil {
			found = append(found, fmt.Sprintf("decoding %s %q: %v", o.GetKind(), o.GetName(), err))
		}
	}

	controllers, controllerProblems := controllerAccounts(deployments, accounts)
	found = append(found, controllerProblems...)
	serveRoles, roleProblems := serveRules(roles, registrations)
	found = append(found, roleProblems...)
	found = append(found, unknownRoles(refs, roles)...)
	found = append(found, bindingProblems(bindings, serveRoles, controllers, registrations)...)
	found = append(found, leaderElectionProblems(deployments, leaseRoles, roleBindings)...)

	return found
}

// inNamespace returns the namespace of an object, which is the release
// namespace when the template sets none.
func inNamespace(objectNamespace, releaseNamespace string) string {
	if objectNamespace == "" {
		return releaseNamespace
	}

	return objectNamespace
}

func account(namespace, name string) string {
	return namespace + "/" + name
}

// controllerAccounts returns the ServiceAccount of each Deployment as
// namespace/name, for each Deployment whose account the chart renders, and a
// problem for a Deployment that runs as an account that the chart does not
// render in the namespace of the Deployment.
func controllerAccounts(deployments []appsv1.Deployment, accounts map[string]bool) ([]string, []string) {
	if len(deployments) == 0 {
		return nil, []string{"the chart renders no Deployment"}
	}

	var controllers, found []string
	for _, d := range deployments {
		name := d.Spec.Template.Spec.ServiceAccountName
		if name == "" {
			name = "default"
		}
		id := account(d.Namespace, name)
		if !accounts[id] {
			found = append(found, fmt.Sprintf(
				"Deployment %q runs as the ServiceAccount %q, which the chart does not render", d.Name, id))
			continue
		}
		controllers = append(controllers, id)
	}

	return controllers, found
}

// serveRules returns what each ClusterRole that grants serve on
// pluginregistrations serves, and a problem for each rule without
// resourceNames and each name beyond the registrations. A rule without
// resourceNames grants serve on every registration of the cluster.
func serveRules(roles []rbacv1.ClusterRole, registrations []string) (map[string]served, []string) {
	serveRoles := map[string]served{}
	var found []string
	for _, role := range roles {
		for _, rule := range role.Rules {
			if !grantsServe(rule) {
				continue
			}
			s := serveRoles[role.Name]
			if len(rule.ResourceNames) == 0 {
				s.all = true
				found = append(found, fmt.Sprintf(
					"ClusterRole %q grants serve on every registration: set resourceNames to the registrations of the chart",
					role.Name,
				))
			}
			for _, name := range rule.ResourceNames {
				if !slices.Contains(registrations, name) {
					found = append(found, fmt.Sprintf(
						"ClusterRole %q grants serve on %q, which is not a registration of the chart", role.Name, name))
				}
				s.names = append(s.names, name)
			}
			serveRoles[role.Name] = s
		}
	}

	return serveRoles, found
}

// unknownRoles returns a problem for each ClusterRoleBinding or RoleBinding
// of a ClusterRole that the chart does not render. A role from outside the
// chart, such as cluster-admin, can grant serve on every registration.
func unknownRoles(refs []roleRef, roles []rbacv1.ClusterRole) []string {
	var found []string
	for _, b := range refs {
		rendered := slices.ContainsFunc(roles, func(r rbacv1.ClusterRole) bool { return r.Name == b.ref.Name })
		if b.ref.Kind == clusterRoleKind && !rendered {
			found = append(found, fmt.Sprintf(
				"%s %q binds the ClusterRole %q, which the chart does not render",
				b.kind,
				b.name,
				b.ref.Name,
			))
		}
	}

	return found
}

func grantsServe(rule rbacv1.PolicyRule) bool {
	return anyOf(rule.APIGroups, pluginsGroup) &&
		anyOf(rule.Resources, registrationsResource) &&
		anyOf(rule.Verbs, serveVerb)
}

// anyOf reports whether values holds want or the wildcard of RBAC.
func anyOf(values []string, want string) bool {
	return slices.Contains(values, want) || slices.Contains(values, rbacv1.ResourceAll)
}

// bindingProblems returns a problem for each binding of a serve ClusterRole
// to a subject that is not a controller ServiceAccount of the chart, for each
// controller that no binding names, and for each registration that the roles
// bound to a controller do not serve.
func bindingProblems(
	bindings []rbacv1.ClusterRoleBinding,
	serveRoles map[string]served,
	controllers []string,
	registrations []string,
) []string {
	var found []string
	granted := map[string]served{}
	bound := map[string]bool{}
	for _, b := range bindings {
		role, ok := serveRoles[b.RoleRef.Name]
		if b.RoleRef.Kind != clusterRoleKind || !ok {
			continue
		}
		for _, s := range b.Subjects {
			id := account(s.Namespace, s.Name)
			if s.Kind != rbacv1.ServiceAccountKind || !slices.Contains(controllers, id) {
				found = append(found, fmt.Sprintf(
					"ClusterRoleBinding %q binds serve to %s %s, which runs no controller of the chart",
					b.Name,
					s.Kind,
					id,
				))
				continue
			}
			bound[id] = true
			g := granted[id]
			g.all = g.all || role.all
			g.names = append(g.names, role.names...)
			granted[id] = g
		}
	}

	for _, id := range controllers {
		if !bound[id] {
			found = append(
				found,
				fmt.Sprintf("no ClusterRoleBinding binds serve to the controller ServiceAccount %s", id),
			)
			continue
		}
		if granted[id].all {
			continue
		}
		for _, name := range registrations {
			if !slices.Contains(granted[id].names, name) {
				found = append(found, fmt.Sprintf(
					"no ClusterRole bound to the controller ServiceAccount %s grants serve on the registration %q",
					id,
					name,
				))
			}
		}
	}

	return found
}

// leaderElectionProblems returns a problem for each Deployment that does not
// pass -leader-elect, and for each one whose ServiceAccount no Role of its
// namespace lets take the leader election Lease. Without election, a rolling
// update runs the old and the new controller as two writers.
func leaderElectionProblems(
	deployments []appsv1.Deployment,
	roles []rbacv1.Role,
	bindings []rbacv1.RoleBinding,
) []string {
	var found []string
	for _, d := range deployments {
		var elect bool
		var lease string
		for _, c := range d.Spec.Template.Spec.Containers {
			if c.Name == controllerContainer {
				elect, lease = leaderElection(c.Args)
			}
		}
		if !elect {
			found = append(found, fmt.Sprintf(
				"the container controller of Deployment %q does not pass -leader-elect, "+
					"so a rolling update runs two writers",
				d.Name,
			))
			continue
		}

		name := cmp.Or(d.Spec.Template.Spec.ServiceAccountName, "default")
		if !leaseGranted(d.Namespace, name, lease, roles, bindings) {
			found = append(found, fmt.Sprintf(
				"no Role bound to the controller ServiceAccount %s in %s grants the leases of leader election",
				account(d.Namespace, name),
				d.Namespace,
			))
		}
	}

	return found
}

// controllerContainer is the name of the container of a plugin Deployment
// that runs the controller.
const controllerContainer = "controller"

// boolFlags are the flags of a plugin command that take no separate value.
var boolFlags = []string{"leader-elect", "version", "zap-devel"}

// leaderElection reads -leader-elect and -leader-election-id from args as the
// flag package of Go does: the last value wins, and parsing stops at "--" or
// at the first argument that is not a flag.
func leaderElection(args []string) (elect bool, lease string) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" || len(arg) < 2 || arg[0] != '-' {
			break
		}

		name, value, hasValue := strings.Cut(strings.TrimPrefix(arg[1:], "-"), "=")
		if !hasValue && !slices.Contains(boolFlags, name) && i+1 < len(args) {
			i++
			value, hasValue = args[i], true
		}
		switch name {
		case "leader-elect":
			elect = true
			if hasValue {
				b, err := strconv.ParseBool(value)
				elect = err == nil && b
			}
		case "leader-election-id":
			lease = value
		}
	}

	return elect, lease
}

// leaseGranted reports whether the Roles bound to the ServiceAccount name in
// namespace let it create a Lease, and get and update the Lease lease.
func leaseGranted(namespace, name, lease string, roles []rbacv1.Role, bindings []rbacv1.RoleBinding) bool {
	var rules []rbacv1.PolicyRule
	for _, b := range bindings {
		bound := slices.ContainsFunc(b.Subjects, func(s rbacv1.Subject) bool {
			return s.Kind == rbacv1.ServiceAccountKind && s.Name == name &&
				inNamespace(s.Namespace, namespace) == namespace
		})
		if b.Namespace != namespace || b.RoleRef.Kind != "Role" || !bound {
			continue
		}
		for _, r := range roles {
			if r.Namespace == namespace && r.Name == b.RoleRef.Name {
				rules = append(rules, r.Rules...)
			}
		}
	}

	allows := func(verb string, names ...string) bool {
		return slices.ContainsFunc(rules, func(rule rbacv1.PolicyRule) bool {
			if !anyOf(rule.APIGroups, "coordination.k8s.io") || !anyOf(rule.Resources, "leases") ||
				!anyOf(rule.Verbs, verb) {
				return false
			}
			return len(rule.ResourceNames) == 0 ||
				slices.ContainsFunc(names, func(n string) bool { return slices.Contains(rule.ResourceNames, n) })
		})
	}

	named := []string{}
	if lease != "" {
		named = append(named, lease)
	}
	// RBAC cannot limit a create by name, so only a rule without names grants it.
	return allows("create") && allows("get", named...) && allows("update", named...)
}
