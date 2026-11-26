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
	"fmt"
	"maps"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// PolicyKinds are the policy kinds that the chart allows. The value is true
// for a kind that the cluster served when the controller started. A kind that
// is not a key is not allowed.
type PolicyKinds map[schema.GroupVersionKind]bool

// sorted returns the kinds of k, sorted so that a setup and a list run in the
// same order each time.
func (k PolicyKinds) sorted() []schema.GroupVersionKind {
	return slices.SortedFunc(maps.Keys(k), func(a, b schema.GroupVersionKind) int {
		return strings.Compare(a.String(), b.String())
	})
}

// ParsePolicyKinds reads the value of the flag -policy-kinds: a list of
// <apiVersion>/<kind>, separated by commas. Each apiVersion needs a group. An
// empty value gives no kinds. The error quotes the first bad entry.
func ParsePolicyKinds(value string) ([]schema.GroupVersionKind, error) {
	if value == "" {
		return nil, nil
	}

	var kinds []schema.GroupVersionKind
	for entry := range strings.SplitSeq(value, ",") {
		apiVersion, kind, ok := strings.CutLast(entry, "/")
		if !ok {
			return nil, fmt.Errorf("the policy kind %q is not <apiVersion>/<kind>", entry)
		}

		gv, err := schema.ParseGroupVersion(apiVersion)
		if err != nil {
			return nil, fmt.Errorf("the policy kind %q has a bad apiVersion: %w", entry, err)
		}

		if kind == "" || gv.Group == "" || gv.Version == "" {
			return nil, fmt.Errorf("the policy kind %q needs a group, a version, and a kind", entry)
		}
		kinds = append(kinds, gv.WithKind(kind))
	}
	return kinds, nil
}

// ResolvePolicyKinds looks up each kind in mapper, in its own version only.
// A kind that mapper does not know is in the result with false. Two versions
// of one kind, a cluster-scoped kind, and any other failed lookup are an
// error.
func ResolvePolicyKinds(mapper meta.RESTMapper, kinds []schema.GroupVersionKind) (PolicyKinds, error) {
	resolved := make(PolicyKinds, len(kinds))
	versions := make(map[schema.GroupKind]string, len(kinds))
	for _, gvk := range kinds {
		// A list of two versions would read each policy twice, and the view
		// in the other version would look stale.
		if version, ok := versions[gvk.GroupKind()]; ok && version != gvk.Version {
			return nil, fmt.Errorf(
				"the policy kinds list %s of %s in the versions %s and %s: list one version of each kind",
				gvk.Kind,
				gvk.Group,
				version,
				gvk.Version,
			)
		}
		versions[gvk.GroupKind()] = gvk.Version

		mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
		switch {
		case meta.IsNoMatchError(err):
			resolved[gvk] = false
			continue
		case err != nil:
			return nil, fmt.Errorf("looking up the policy kind %s: %w", gvk, err)
		}

		if mapping.Scope.Name() != meta.RESTScopeNameNamespace {
			return nil, fmt.Errorf(
				"the policy kind %s is cluster-scoped: a policy of a route must be in the namespace of the route",
				gvk,
			)
		}
		resolved[gvk] = true
	}
	return resolved, nil
}
