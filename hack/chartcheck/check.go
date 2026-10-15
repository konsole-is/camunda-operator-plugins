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
	"fmt"
	"slices"

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
		case "ClusterRoleBinding":
			var b rbacv1.ClusterRoleBinding
			err = runtime.DefaultUnstructuredConverter.FromUnstructured(o.Object, &b)
			bindings = append(bindings, b)
		}
		if err != nil {
			found = append(found, fmt.Sprintf("decoding %s %q: %v", o.GetKind(), o.GetName(), err))
		}
	}

	controllers, controllerProblems := controllerAccounts(deployments, accounts)
	found = append(found, controllerProblems...)
	serveRoles, roleProblems := serveRules(roles, registrations)
	found = append(found, roleProblems...)
	found = append(found, unknownRoles(bindings, roles)...)
	found = append(found, bindingProblems(bindings, serveRoles, controllers, registrations)...)

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

// unknownRoles returns a problem for each binding of a ClusterRole that the
// chart does not render. A role from outside the chart, such as
// cluster-admin, can grant serve on every registration.
func unknownRoles(bindings []rbacv1.ClusterRoleBinding, roles []rbacv1.ClusterRole) []string {
	var found []string
	for _, b := range bindings {
		rendered := slices.ContainsFunc(roles, func(r rbacv1.ClusterRole) bool { return r.Name == b.RoleRef.Name })
		if b.RoleRef.Kind == clusterRoleKind && !rendered {
			found = append(found, fmt.Sprintf(
				"ClusterRoleBinding %q binds the ClusterRole %q, which the chart does not render",
				b.Name,
				b.RoleRef.Name,
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
