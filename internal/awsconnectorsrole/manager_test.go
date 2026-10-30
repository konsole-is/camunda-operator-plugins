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

package awsconnectorsrole

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit/awsfake"
)

func validOptions() Options {
	return Options{
		Registrations: []pluginsv1alpha1.PluginRegistration{
			{ObjectMeta: metav1.ObjectMeta{Name: RegistrationRole}},
			{ObjectMeta: metav1.ObjectMeta{Name: RegistrationExistingRole}},
		},
		Mode:                   awskit.ModeIRSA,
		RoleNamePrefix:         "camunda-connectors",
		PermissionsBoundaryARN: "arn:aws:iam::111122223333:policy/b",
		OIDCProviderARN:        "arn:aws:iam::111122223333:oidc-provider/oidc.eks.eu-west-1.amazonaws.com/id/ABC",
		EKSClusterName:         "eks-1",
		Clients:                awskit.Clients{IAM: awsfake.NewIAM(), EKS: awsfake.NewEKS(), STS: awsfake.NewSTS()},
	}
}

func TestNewPluginRefuses(t *testing.T) {
	for name, tc := range map[string]struct {
		change func(*Options)
		want   string
	}{
		"an unknown registration": {func(o *Options) {
			o.Registrations = append(o.Registrations, pluginsv1alpha1.PluginRegistration{
				ObjectMeta: metav1.ObjectMeta{Name: "aws-other"},
			})
		}, `"aws-other"`},
		"an unknown mode":              {func(o *Options) { o.Mode = "kiam" }, "mode"},
		"IRSA without a provider":      {func(o *Options) { o.OIDCProviderARN = "" }, "OIDC provider"},
		"Pod Identity without cluster": {func(o *Options) { o.Mode = awskit.ModePodIdentity; o.EKSClusterName = "" }, "EKS cluster name"},
		"Pod Identity without EKS": {func(o *Options) {
			o.Mode = awskit.ModePodIdentity
			o.Clients.EKS = nil
		}, "EKS client"},
		"no IAM client":               {func(o *Options) { o.Clients.IAM = nil }, "IAM"},
		"a role without a boundary":   {func(o *Options) { o.PermissionsBoundaryARN = "" }, "permissions boundary"},
		"an empty prefix":             {func(o *Options) { o.RoleNamePrefix = "" }, "role name prefix"},
		"a prefix of 48 characters":   {func(o *Options) { o.RoleNamePrefix = strings.Repeat("a", 48) }, "role name prefix"},
		"a prefix with a slash":       {func(o *Options) { o.RoleNamePrefix = "cam/x" }, "role name prefix"},
		"the prefix of aws-identity":  {func(o *Options) { o.RoleNamePrefix = "camunda" }, "aws-identity"},
		"that prefix in another case": {func(o *Options) { o.RoleNamePrefix = "Camunda" }, "aws-identity"},
	} {
		t.Run(name, func(t *testing.T) {
			opts := validOptions()
			tc.change(&opts)

			_, err := newPlugin(opts)

			assert.ErrorContains(t, err, tc.want)
		})
	}
}

func TestNewPluginAcceptsTheExistingRoleAloneWithoutBoundaryOrPrefix(t *testing.T) {
	opts := validOptions()
	opts.Registrations = opts.Registrations[1:]
	opts.PermissionsBoundaryARN = ""
	opts.RoleNamePrefix = ""

	p, err := newPlugin(opts)

	require.NoError(t, err)
	assert.Empty(t, p.boundary)
}

func TestNewPluginAcceptsValidOptions(t *testing.T) {
	p, err := newPlugin(validOptions())

	require.NoError(t, err)
	assert.Equal(t, "camunda-connectors", p.prefix)
	assert.Equal(t, "oidc.eks.eu-west-1.amazonaws.com/id/ABC", p.provider.Issuer)
}
