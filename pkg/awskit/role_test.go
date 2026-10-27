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

package awskit_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit/awsfake"
)

const (
	boundary      = "arn:aws:iam::111122223333:policy/camunda-boundary"
	otherBoundary = "arn:aws:iam::111122223333:policy/other-boundary"
)

func roleSpec() awskit.RoleSpec {
	return awskit.RoleSpec{
		Name:        "camunda-0123456789abcdef",
		Description: "Camunda cluster team-a/orders",
		TrustPolicy: []byte(
			`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sts:AssumeRole"}]}`,
		),
		PermissionsBoundary: boundary,
		Tags:                awskit.RequestTags("team-a", "orders", "orders-identity"),
	}
}

func TestEnsureRoleCreatesARoleWithTrustBoundaryAndTags(t *testing.T) {
	f := awsfake.NewIAM()

	role, change, err := awskit.EnsureRole(context.Background(), f, roleSpec())

	require.NoError(t, err)
	assert.Equal(t, awskit.RoleChange{Created: true}, change)
	assert.Equal(t, awskit.Role{
		Name: "camunda-0123456789abcdef",
		ARN:  "arn:aws:iam::111122223333:role/camunda-0123456789abcdef",
	}, role)
	got, ok := f.Role("camunda-0123456789abcdef")
	require.True(t, ok)
	assert.JSONEq(t, string(roleSpec().TrustPolicy), got.TrustPolicy)
	assert.Equal(t, boundary, got.PermissionsBoundary)
	assert.Equal(t, roleSpec().Tags, got.Tags)
	assert.Equal(t, "Camunda cluster team-a/orders", got.Description)
}

func TestEnsureRoleLeavesAnUnchangedRoleAlone(t *testing.T) {
	f := awsfake.NewIAM()
	_, _, err := awskit.EnsureRole(context.Background(), f, roleSpec())
	require.NoError(t, err)
	before := len(f.Calls())

	_, change, err := awskit.EnsureRole(context.Background(), f, roleSpec())

	require.NoError(t, err)
	assert.Equal(t, awskit.RoleChange{}, change)
	assert.Equal(t, []string{"GetRole"}, f.Calls()[before:])
}

func TestEnsureRoleUpdatesTheTrustBoundaryAndTags(t *testing.T) {
	f := awsfake.NewIAM()
	_, _, err := awskit.EnsureRole(context.Background(), f, roleSpec())
	require.NoError(t, err)
	spec := roleSpec()
	spec.TrustPolicy = []byte(`{"Version":"2012-10-17","Statement":[]}`)
	spec.PermissionsBoundary = otherBoundary
	spec.Tags = awskit.RequestTags("team-a", "orders", "orders-identity-2")

	_, change, err := awskit.EnsureRole(context.Background(), f, spec)

	require.NoError(t, err)
	assert.Equal(t, awskit.RoleChange{Updated: []string{"trust policy", "permissions boundary", "tags"}}, change)
	got, _ := f.Role(spec.Name)
	assert.JSONEq(t, `{"Version":"2012-10-17","Statement":[]}`, got.TrustPolicy)
	assert.Equal(t, otherBoundary, got.PermissionsBoundary)
	assert.Equal(t, "orders-identity-2", got.Tags[awskit.TagRequest])
}

func TestEnsureRoleKeepsATagThatTheSpecDoesNotName(t *testing.T) {
	f := awsfake.NewIAM()
	spec := roleSpec()
	spec.Tags = map[string]string{"owner": "admin", awskit.TagClaim: "orders"}
	_, _, err := awskit.EnsureRole(context.Background(), f, spec)
	require.NoError(t, err)

	_, change, err := awskit.EnsureRole(context.Background(), f, roleSpec())

	require.NoError(t, err)
	assert.Equal(t, []string{"tags"}, change.Updated)
	got, _ := f.Role(spec.Name)
	assert.Equal(t, "admin", got.Tags["owner"])
}

func TestEnsureRoleReadsARoleThatAnotherWriterCreated(t *testing.T) {
	f := awsfake.NewIAM()
	_, _, err := awskit.EnsureRole(context.Background(), f, roleSpec())
	require.NoError(t, err)
	f.FailNext("GetRole", &smithy.GenericAPIError{Code: "NoSuchEntity"})

	role, change, err := awskit.EnsureRole(context.Background(), f, roleSpec())

	require.NoError(t, err)
	assert.False(t, change.Created)
	assert.Equal(t, "arn:aws:iam::111122223333:role/camunda-0123456789abcdef", role.ARN)
}

func TestEnsureRoleReturnsAFailedCall(t *testing.T) {
	f := awsfake.NewIAM()
	boom := errors.New("boom")
	f.FailNext("CreateRole", boom)

	_, _, err := awskit.EnsureRole(context.Background(), f, roleSpec())

	assert.ErrorIs(t, err, boom)
}

func TestDeleteRoleDeletesEveryPage(t *testing.T) {
	f := awsfake.NewIAM()
	f.PageSize = 1
	_, _, err := awskit.EnsureRole(context.Background(), f, roleSpec())
	require.NoError(t, err)
	for _, name := range []string{"a", "b", "c"} {
		require.NoError(t, awskit.PutInlinePolicy(context.Background(), f, roleSpec().Name, name, []byte(`{}`)))
	}
	for _, arn := range []string{"arn:aws:iam::1:policy/x", "arn:aws:iam::1:policy/y"} {
		f.AddPolicy(arn)
		f.AttachPolicy(roleSpec().Name, arn)
	}

	deleted, err := awskit.DeleteRole(context.Background(), f, roleSpec().Name)

	require.NoError(t, err)
	assert.True(t, deleted)
	assert.Empty(t, f.Roles())
}

func TestDeleteRoleOfAMissingRoleReportsFalse(t *testing.T) {
	deleted, err := awskit.DeleteRole(context.Background(), awsfake.NewIAM(), "gone")

	require.NoError(t, err)
	assert.False(t, deleted)
}

func TestInlinePoliciesReadsEveryPage(t *testing.T) {
	f := awsfake.NewIAM()
	f.PageSize = 1
	_, _, err := awskit.EnsureRole(context.Background(), f, roleSpec())
	require.NoError(t, err)
	docs := map[string]string{"a": `{"a":"x y"}`, "b": `{"b":1}`, "c": `{"c":[]}`}
	for name, doc := range docs {
		require.NoError(t, awskit.PutInlinePolicy(context.Background(), f, roleSpec().Name, name, []byte(doc)))
	}

	got, err := awskit.InlinePolicies(context.Background(), f, roleSpec().Name)

	require.NoError(t, err)
	require.Len(t, got, 3)
	for name, doc := range docs {
		assert.JSONEq(t, doc, string(got[name]), name)
	}
}

func TestDeleteInlinePolicyIgnoresAMissingPolicy(t *testing.T) {
	f := awsfake.NewIAM()
	_, _, err := awskit.EnsureRole(context.Background(), f, roleSpec())
	require.NoError(t, err)

	assert.NoError(t, awskit.DeleteInlinePolicy(context.Background(), f, roleSpec().Name, "gone"))
}
