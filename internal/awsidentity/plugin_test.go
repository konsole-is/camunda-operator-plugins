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
	"testing"

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
	assert.Empty(t, fake.Calls())
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
