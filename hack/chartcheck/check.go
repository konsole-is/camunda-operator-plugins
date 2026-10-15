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
)

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
			accounts[o.GetName()] = true
		case "Deployment":
			var d appsv1.Deployment
			err = runtime.DefaultUnstructuredConverter.FromUnstructured(o.Object, &d)
			deployments = append(deployments, d)
		case "ClusterRole":
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
	found = append(found, bindingProblems(bindings, serveRoles, controllers, namespace)...)

	return found
}

// controllerAccounts returns the ServiceAccount of each Deployment, and a
// problem for a Deployment that runs as an account the chart does not render.
func controllerAccounts(deployments []appsv1.Deployment, accounts map[string]bool) ([]string, []string) {
	if len(deployments) == 0 {
		return nil, []string{"the chart renders no Deployment"}
	}

	var controllers, found []string
	for _, d := range deployments {
		name := d.Spec.Template.Spec.ServiceAccountName
		if !accounts[name] {
			found = append(found, fmt.Sprintf(
				"Deployment %q runs as the ServiceAccount %q, which the chart does not render", d.Name, name))
			continue
		}
		controllers = append(controllers, name)
	}

	return controllers, found
}

// serveRules returns the names of the ClusterRoles that grant serve on
// pluginregistrations, and a problem for each registration they miss and
// each name they grant beyond the registrations. A rule without
// resourceNames grants serve on every registration of the cluster.
func serveRules(roles []rbacv1.ClusterRole, registrations []string) (map[string]bool, []string) {
	serveRoles := map[string]bool{}
	served := map[string]bool{}
	var found []string
	for _, role := range roles {
		for _, rule := range role.Rules {
			if !grantsServe(rule) {
				continue
			}
			serveRoles[role.Name] = true
			if len(rule.ResourceNames) == 0 {
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
				served[name] = true
			}
		}
	}

	for _, name := range registrations {
		if !served[name] {
			found = append(found, fmt.Sprintf("no ClusterRole grants serve on the registration %q", name))
		}
	}

	return serveRoles, found
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
// to a subject that is not a controller ServiceAccount of the chart, and for
// each controller ServiceAccount that no binding names.
func bindingProblems(
	bindings []rbacv1.ClusterRoleBinding,
	serveRoles map[string]bool,
	controllers []string,
	namespace string,
) []string {
	var found []string
	bound := map[string]bool{}
	for _, b := range bindings {
		if b.RoleRef.Kind != "ClusterRole" || !serveRoles[b.RoleRef.Name] {
			continue
		}
		for _, s := range b.Subjects {
			if s.Kind == rbacv1.ServiceAccountKind && s.Namespace == namespace && slices.Contains(controllers, s.Name) {
				bound[s.Name] = true
				continue
			}
			found = append(found, fmt.Sprintf(
				"ClusterRoleBinding %q binds serve to %s %s/%s, which runs no controller of the chart",
				b.Name, s.Kind, s.Namespace, s.Name))
		}
	}

	for _, name := range controllers {
		if !bound[name] {
			found = append(found, fmt.Sprintf(
				"no ClusterRoleBinding binds serve to the controller ServiceAccount %s/%s", namespace, name))
		}
	}

	return found
}
