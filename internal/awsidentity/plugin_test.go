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

package awsidentity

import (
	"context"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit/awsfake"
	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

const (
	testBoundary = "arn:aws:iam::111122223333:policy/camunda-boundary"
	testProvider = "arn:aws:iam::111122223333:oidc-provider/oidc.eks.eu-west-1.amazonaws.com/id/ABC"
)

func validOptions() Options {
	return Options{
		Mode:                   awskit.ModeIRSA,
		PermissionsBoundaryARN: testBoundary,
		AllowedActions:         []string{"s3:*"},
		OIDCProviderARN:        testProvider,
		EKSClusterName:         "eks-1",
		Clients:                awskit.Clients{IAM: awsfake.NewIAM(), EKS: awsfake.NewEKS(), STS: awsfake.NewSTS()},
	}
}

func TestReconcileWaitsForAServiceAccount(t *testing.T) {
	fake := awsfake.NewIAM()
	opts := validOptions()
	opts.Clients.IAM = fake
	p, err := newPlugin(opts)
	require.NoError(t, err)

	res, err := p.Reconcile(context.Background(), pluginkit.Request[Params, map[string]string]{
		Object: &pluginsv1alpha1.PluginRequest{
			ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "orders-identity"},
			Spec: pluginsv1alpha1.PluginRequestSpec{
				Target: pluginsv1alpha1.TargetRef{Kind: "CamundaCluster", Name: "orders"},
			},
		},
	})

	require.NoError(t, err)
	require.NotNil(t, res.Pending)
	assert.Equal(t, ReasonMissingServiceAccount, res.Pending.Reason)
	assert.Equal(t, []string{"GetRole"}, fake.Calls(), "a request without a role gets none")
}

func newTestRole(t *testing.T) (*Plugin, *awsfake.IAM, awskit.Role) {
	fake := awsfake.NewIAM()
	opts := validOptions()
	opts.Clients.IAM = fake
	p, err := newPlugin(opts)
	require.NoError(t, err)
	role, _, err := awskit.EnsureRole(context.Background(), fake, awskit.RoleSpec{
		Name:        "camunda-r",
		TrustPolicy: []byte(`{"Version":"2012-10-17","Statement":[]}`),
	})
	require.NoError(t, err)
	return p, fake, role
}

func TestWritePoliciesFreesTheSpaceOfARemovedPolicyBeforeItPuts(t *testing.T) {
	p, fake, role := newTestRole(t)
	old, replacement := policyOfSize(t, 6000), policyOfSize(t, 5000)
	require.NoError(t, awskit.PutInlinePolicy(context.Background(), fake, role.Name, "grant-old", old))

	plan, put, removed, err := p.writePolicies(
		context.Background(),
		role,
		map[string][]byte{"grant-old": old},
		[]grantPolicy{{grant: "new", policy: "grant-new", doc: replacement}},
	)

	require.NoError(t, err)
	assert.Equal(t, []string{"grant-new"}, put)
	assert.Equal(t, []string{"grant-old"}, removed)
	assert.Equal(t, metav1.ConditionTrue, plan.applied["new"].status, plan.applied["new"].message)
	got, _ := fake.Role(role.Name)
	assert.Equal(t, []string{"grant-new"}, slices.Collect(maps.Keys(got.InlinePolicies)))
}

func TestWritePoliciesAppliesAGrowthThatAShrinkMadeRoomFor(t *testing.T) {
	p, fake, role := newTestRole(t)
	a, b := policyOfSize(t, 5000), policyOfSize(t, 5000)
	require.NoError(t, awskit.PutInlinePolicy(context.Background(), fake, role.Name, "grant-a", a))
	require.NoError(t, awskit.PutInlinePolicy(context.Background(), fake, role.Name, "grant-b", b))

	plan, put, _, err := p.writePolicies(
		context.Background(),
		role,
		map[string][]byte{"grant-a": a, "grant-b": b},
		[]grantPolicy{
			{grant: "a", policy: "grant-a", doc: policyOfSize(t, 6000)},
			{grant: "b", policy: "grant-b", doc: policyOfSize(t, 4000)},
		},
	)

	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"grant-a", "grant-b"}, put)
	assert.Equal(t, metav1.ConditionTrue, plan.applied["a"].status, plan.applied["a"].message)
	assert.Equal(t, 10000, plan.size)
}

func TestWritePoliciesPlansAgainAfterIAMRefusesADocument(t *testing.T) {
	p, fake, role := newTestRole(t)
	fake.FailNext("PutRolePolicy", &smithy.GenericAPIError{Code: "MalformedPolicyDocument", Message: "bad"})
	big, small := policyOfSize(t, 10000), policyOfSize(t, 500)

	plan, put, _, err := p.writePolicies(context.Background(), role, map[string][]byte{}, []grantPolicy{
		{grant: "big", policy: "grant-big", doc: big},
		{grant: "small", policy: "grant-small", doc: small},
	})

	require.NoError(t, err)
	assert.Equal(t, []string{"grant-small"}, put)
	assert.Equal(t, pluginsv1alpha1.ReasonRejected, plan.applied["big"].reason)
	assert.Contains(t, plan.applied["big"].message, "bad")
	assert.Equal(t, metav1.ConditionTrue, plan.applied["small"].status, plan.applied["small"].message)
	assert.Equal(t, 500, plan.size)
}

func TestRoleNameDiffersForTwoEKSClusters(t *testing.T) {
	request := &pluginsv1alpha1.PluginRequest{
		ObjectMeta: metav1.ObjectMeta{Namespace: "team-a"},
		Spec:       pluginsv1alpha1.PluginRequestSpec{ClaimRef: pluginsv1alpha1.ClaimRef{Name: "orders"}},
	}
	other := validOptions()
	other.OIDCProviderARN = "arn:aws:iam::111122223333:oidc-provider/oidc.eks.eu-west-1.amazonaws.com/id/DEF"
	podIdentity := validOptions()
	podIdentity.Mode = awskit.ModePodIdentity
	names := map[string]bool{}

	for _, opts := range []Options{validOptions(), other, podIdentity} {
		p, err := newPlugin(opts)
		require.NoError(t, err)
		names[p.roleName(request)] = true
	}

	assert.Len(t, names, 3)
}

func TestNewPluginAcceptsEachMode(t *testing.T) {
	_, err := newPlugin(validOptions())
	assert.NoError(t, err)

	opts := validOptions()
	opts.Mode, opts.OIDCProviderARN = awskit.ModePodIdentity, ""
	_, err = newPlugin(opts)
	assert.NoError(t, err)
}

func TestNewPluginRefusesBadOptions(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Options)
		want   string
	}{
		{"an unknown mode", func(o *Options) { o.Mode = "other" }, `"other"`},
		{"no boundary", func(o *Options) { o.PermissionsBoundaryARN = "" }, "permissions boundary"},
		{"no allowed actions", func(o *Options) { o.AllowedActions = nil }, "allowed action"},
		{"a bad allowed action", func(o *Options) { o.AllowedActions = []string{"s3"} }, `"s3"`},
		{"IRSA with another ARN", func(o *Options) { o.OIDCProviderARN = "arn:aws:iam::1:role/x" }, "OIDC provider"},
		{"Pod Identity without a cluster", func(o *Options) {
			o.Mode, o.EKSClusterName = awskit.ModePodIdentity, ""
		}, "EKS cluster name"},
		{"Pod Identity without EKS", func(o *Options) {
			o.Mode, o.Clients.EKS = awskit.ModePodIdentity, nil
		}, "EKS client"},
		{"no IAM client", func(o *Options) { o.Clients.IAM = nil }, "IAM"},
	} {
		opts := validOptions()
		tc.change(&opts)

		_, err := newPlugin(opts)

		assert.ErrorContains(t, err, tc.want, tc.name)
	}
}

func TestHealthChecksTheCredentialsAndTheBoundary(t *testing.T) {
	p, err := newPlugin(validOptions())
	require.NoError(t, err)

	assert.ErrorContains(t, p.health(context.Background()), testBoundary, "the fake has no boundary")

	p.clients.IAM.(*awsfake.IAM).AddPolicy(testBoundary)
	assert.NoError(t, p.health(context.Background()))
}

func TestWritePoliciesGivesTheNameOfARefusedHolderToTheOtherGrant(t *testing.T) {
	p, _, role := newTestRole(t)
	long := strings.Repeat("a", 200)
	alias := strings.TrimPrefix(policyName(long), policyPrefix)

	type result struct {
		plan policyPlan
		put  []string
		err  error
	}
	// IAM refuses the document of the holder each time, as it is not JSON.
	policies := []grantPolicy{
		{grant: long, policy: policyName(long), doc: policyOfSize(t, 100)},
		{grant: alias, policy: policyName(alias), doc: []byte("not json")},
	}
	done := make(chan result, 1)
	go func() {
		plan, put, _, err := p.writePolicies(context.Background(), role, map[string][]byte{}, policies)
		done <- result{plan, put, err}
	}()

	select {
	case r := <-done:
		// The refused holder gives up the name, so the other Grant gets it.
		require.NoError(t, r.err)
		assert.Equal(t, []string{policyName(long)}, r.put)
		assert.Equal(t, pluginsv1alpha1.ReasonRejected, r.plan.applied[alias].reason)
		assert.Contains(t, r.plan.applied[alias].message, "IAM refused the policy")
		assert.Equal(t, metav1.ConditionTrue, r.plan.applied[long].status)
	case <-time.After(5 * time.Second):
		assert.Fail(t, "writePolicies did not return")
	}
}
