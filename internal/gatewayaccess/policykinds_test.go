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
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var securityPolicy = schema.GroupVersionKind{
	Group:   "gateway.envoyproxy.io",
	Version: "v1alpha1",
	Kind:    "SecurityPolicy",
}

// failingMapper answers every lookup with an error that is not a missing
// kind, as a failed discovery does.
type failingMapper struct {
	meta.RESTMapper
}

func (failingMapper) RESTMapping(schema.GroupKind, ...string) (*meta.RESTMapping, error) {
	return nil, errors.New("discovery is down")
}

func TestParsePolicyKinds_ReadsEachKindOfTheList(t *testing.T) {
	got, err := ParsePolicyKinds(
		"gateway.envoyproxy.io/v1alpha1/SecurityPolicy,security.istio.io/v1/AuthorizationPolicy",
	)
	require.NoError(t, err)
	assert.Equal(t, []schema.GroupVersionKind{
		securityPolicy,
		{Group: "security.istio.io", Version: "v1", Kind: "AuthorizationPolicy"},
	}, got)
}

func TestParsePolicyKinds_EmptyIsNone(t *testing.T) {
	got, err := ParsePolicyKinds("")
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestParsePolicyKinds_RefusesAKindWithoutAGroup(t *testing.T) {
	_, err := ParsePolicyKinds("v1/ConfigMap")
	assert.ErrorContains(t, err, `"v1/ConfigMap"`)
}

func TestParsePolicyKinds_RefusesAnEntryWithoutAKind(t *testing.T) {
	_, err := ParsePolicyKinds("gateway.envoyproxy.io/v1alpha1/")
	assert.ErrorContains(t, err, `"gateway.envoyproxy.io/v1alpha1/"`)
}

func TestParsePolicyKinds_RefusesAnEntryWithoutAVersion(t *testing.T) {
	_, err := ParsePolicyKinds("gateway.envoyproxy.io/SecurityPolicy")
	assert.ErrorContains(t, err, `"gateway.envoyproxy.io/SecurityPolicy"`)
}

func TestResolvePolicyKinds_MarksAServedKindAndAKindWithoutCRD(t *testing.T) {
	mapper := meta.NewDefaultRESTMapper(nil)
	mapper.Add(securityPolicy, meta.RESTScopeNamespace)
	missing := schema.GroupVersionKind{Group: "policies.example.com", Version: "v1", Kind: "MissingPolicy"}

	got, err := ResolvePolicyKinds(mapper, []schema.GroupVersionKind{securityPolicy, missing})

	require.NoError(t, err)
	assert.Equal(t, PolicyKinds{securityPolicy: true, missing: false}, got)
}

func TestResolvePolicyKinds_ServedInAnotherVersionOnlyIsNotServed(t *testing.T) {
	mapper := meta.NewDefaultRESTMapper(nil)
	mapper.Add(
		schema.GroupVersionKind{Group: securityPolicy.Group, Version: "v1", Kind: securityPolicy.Kind},
		meta.RESTScopeNamespace,
	)

	got, err := ResolvePolicyKinds(mapper, []schema.GroupVersionKind{securityPolicy})

	require.NoError(t, err)
	assert.Equal(t, PolicyKinds{securityPolicy: false}, got)
}

func TestResolvePolicyKinds_RefusesAClusterScopedKind(t *testing.T) {
	mapper := meta.NewDefaultRESTMapper(nil)
	mapper.Add(securityPolicy, meta.RESTScopeRoot)

	_, err := ResolvePolicyKinds(mapper, []schema.GroupVersionKind{securityPolicy})

	assert.ErrorContains(t, err, "cluster-scoped")
}

func TestResolvePolicyKinds_ReturnsAFailedLookup(t *testing.T) {
	_, err := ResolvePolicyKinds(failingMapper{}, []schema.GroupVersionKind{securityPolicy})
	assert.ErrorContains(t, err, "discovery is down")
}

func TestResolvePolicyKinds_RefusesTwoVersionsOfOneKind(t *testing.T) {
	v1 := schema.GroupVersionKind{Group: securityPolicy.Group, Version: "v1", Kind: securityPolicy.Kind}
	mapper := meta.NewDefaultRESTMapper(nil)
	mapper.Add(securityPolicy, meta.RESTScopeNamespace)
	mapper.Add(v1, meta.RESTScopeNamespace)

	_, err := ResolvePolicyKinds(mapper, []schema.GroupVersionKind{securityPolicy, v1})

	assert.ErrorContains(t, err, "SecurityPolicy")
	assert.ErrorContains(t, err, "one version")
}
