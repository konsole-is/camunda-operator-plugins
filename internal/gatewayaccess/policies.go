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
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utiljson "k8s.io/apimachinery/pkg/util/json"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"

	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

// The parameters that a policy template cannot name.
const (
	parameterPolicyTemplates = "policyTemplates"
	parameterRouteFilters    = "routeFilters"
)

// PolicyTemplate is one item of the parameter policyTemplates: the kind of a
// policy and its spec, without its targets. A string of the spec that is
// exactly {<parameter>} takes the value of that parameter of the request.
type PolicyTemplate struct {
	APIVersion string          `json:"apiVersion"`
	Kind       string          `json:"kind"`
	Spec       json.RawMessage `json:"spec"`
}

// PolicyKey identifies a policy of a request. Two kinds with one name are two
// policies.
type PolicyKey struct {
	GVK  schema.GroupVersionKind
	Name string
}

// Policies returns one policy for each template of p and each route of
// routes, in the order of the templates and then of the routes. Each policy
// is PolicyName of its route and its template, and its spec.targetRefs names
// its route. The policies have no namespace and no labels. When a template
// breaks a rule, Policies returns no policy and the refusal of the first such
// template, also when routes is empty: the reason PolicyKindNotAllowed for a
// kind that kinds does not hold, PolicyKindNotInstalled for a kind that kinds
// marks as not served, and InvalidParameters for a spec that is not an
// object, that sets targetRefs, or that names no parameter or one without a
// value.
func Policies(p Params, routes []client.Object, kinds PolicyKinds) ([]*unstructured.Unstructured, *pluginkit.Waiting) {
	var policies []*unstructured.Unstructured
	for i, t := range p.PolicyTemplates {
		gvk, spec, refused := resolveTemplate(p, i, t, kinds)
		if refused != nil {
			return nil, refused
		}

		for _, route := range routes {
			policies = append(policies, policyOf(gvk, spec, i, route))
		}
	}
	return policies, nil
}

// resolveTemplate returns the kind of the template at index i of p and its
// spec with each reference resolved, or why the template is refused.
func resolveTemplate(
	p Params,
	i int,
	t PolicyTemplate,
	kinds PolicyKinds,
) (schema.GroupVersionKind, map[string]any, *pluginkit.Waiting) {
	at := fmt.Sprintf("%s[%d]", parameterPolicyTemplates, i)
	invalid := func(format string, args ...any) (schema.GroupVersionKind, map[string]any, *pluginkit.Waiting) {
		return schema.GroupVersionKind{}, nil, &pluginkit.Waiting{
			Reason:  pluginsv1alpha1.ReasonInvalidParameters,
			Message: fmt.Sprintf(format, args...),
		}
	}

	gv, err := schema.ParseGroupVersion(t.APIVersion)
	if err != nil {
		return invalid("%s has the apiVersion %q, which is not valid: %v", at, t.APIVersion, err)
	}

	gvk := gv.WithKind(t.Kind)
	served, allowed := kinds[gvk]
	switch {
	case !allowed:
		return schema.GroupVersionKind{}, nil, &pluginkit.Waiting{
			Reason: ReasonPolicyKindNotAllowed,
			Message: fmt.Sprintf(
				"%s has the kind %s of %s, which the chart value policyKinds does not list",
				at,
				t.Kind,
				t.APIVersion,
			),
		}
	case !served:
		return schema.GroupVersionKind{}, nil, &pluginkit.Waiting{
			Reason: ReasonPolicyKindNotInstalled,
			Message: fmt.Sprintf(
				"%s: the cluster did not serve %s of %s when the controller started. "+
					"Install its CRD, then restart the controller",
				at,
				t.Kind,
				t.APIVersion,
			),
		}
	}

	var decoded any
	if err := utiljson.Unmarshal(t.Spec, &decoded); err != nil {
		return invalid("%s.spec is not an object: %v", at, err)
	}

	spec, ok := decoded.(map[string]any)
	if !ok {
		return invalid("%s.spec is not an object", at)
	}

	if _, ok := spec["targetRefs"]; ok {
		return invalid("%s.spec.targetRefs is set: the plugin sets the targets of each policy", at)
	}

	resolved, err := resolveReferences(spec, at+".spec", p.values)
	if err != nil {
		return invalid("%s", err)
	}

	return gvk, resolved.(map[string]any), nil
}

// resolveReferences returns a copy of value in which each string that is
// exactly {<parameter>} is a copy of the value of that parameter in values.
// path is the place of value in the parameters, for the error.
func resolveReferences(value any, path string, values map[string]any) (any, error) {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		// Sorted, so that the error names the same field each time.
		for _, k := range slices.Sorted(maps.Keys(v)) {
			resolved, err := resolveReferences(v[k], path+"."+k, values)
			if err != nil {
				return nil, err
			}
			out[k] = resolved
		}
		return out, nil
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			resolved, err := resolveReferences(item, path+"["+strconv.Itoa(i)+"]", values)
			if err != nil {
				return nil, err
			}
			out[i] = resolved
		}
		return out, nil
	case string:
		return resolveReference(v, path, values)
	}
	return value, nil
}

func resolveReference(s, path string, values map[string]any) (any, error) {
	name, ok := strings.CutPrefix(s, "{")
	if ok {
		name, ok = strings.CutSuffix(name, "}")
	}
	if !ok || name == "" || strings.ContainsAny(name, "{}") {
		return s, nil
	}

	if name == parameterPolicyTemplates || name == parameterRouteFilters {
		return nil, fmt.Errorf("%s: %s names the parameter %s, which a policy template cannot take", path, s, name)
	}

	value, found := values[name]
	switch {
	case !found:
		return nil, fmt.Errorf("%s: %s names no parameter of the request", path, s)
	case value == nil:
		return nil, fmt.Errorf("%s: %s names the parameter %s, which has no value", path, s, name)
	}
	return runtime.DeepCopyJSONValue(value), nil
}

// policyOf returns the policy of the template at index i for route, with the
// kind gvk and a copy of spec.
func policyOf(gvk schema.GroupVersionKind, spec map[string]any, i int, route client.Object) *unstructured.Unstructured {
	content := runtime.DeepCopyJSON(spec)
	content["targetRefs"] = []any{map[string]any{
		"group": gatewayv1.GroupName,
		"kind":  KeyOf(route).Kind,
		"name":  route.GetName(),
	}}

	policy := &unstructured.Unstructured{Object: map[string]any{"spec": content}}
	policy.SetGroupVersionKind(gvk)
	policy.SetName(PolicyName(route.GetName(), i))
	return policy
}

// KeyOfPolicy returns the key of a policy. obj needs its apiVersion and kind.
func KeyOfPolicy(obj *unstructured.Unstructured) PolicyKey {
	return PolicyKey{GVK: obj.GroupVersionKind(), Name: obj.GetName()}
}

// PolicyName is the name of the policy of the template at index for the
// route named route.
func PolicyName(route string, index int) string {
	return route + "-policy-" + strconv.Itoa(index)
}
