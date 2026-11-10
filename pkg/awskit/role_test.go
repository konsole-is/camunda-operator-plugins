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

	"github.com/aws/aws-sdk-go-v2/service/iam"
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
		Tags:                awskit.ExtraTags(nil).OwnerTags("team-a", "orders", "orders-identity", "uid-1"),
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
	spec.Tags = awskit.ExtraTags(nil).OwnerTags("team-a", "orders", "orders-identity-2", "uid-1")

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
	spec.Tags = map[string]string{
		"owner":              "admin",
		awskit.TagNamespace:  "team-a",
		awskit.TagRequest:    "orders-identity",
		awskit.TagRequestUID: "uid-1",
	}
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

func TestEnsureRoleRefusesARoleOfAnotherRequest(t *testing.T) {
	f := awsfake.NewIAM()
	other := roleSpec()
	other.Tags = awskit.ExtraTags(nil).OwnerTags("team-a", "orders", "orders-identity", "uid-2")
	_, _, err := awskit.EnsureRole(context.Background(), f, other)
	require.NoError(t, err)
	before := len(f.Calls())

	_, _, err = awskit.EnsureRole(context.Background(), f, roleSpec())

	var inUse awskit.RoleInUseError
	require.ErrorAs(t, err, &inUse)
	assert.Equal(t, roleSpec().Name, inUse.Name)
	assert.Equal(t, "the request team-a/orders-identity with the UID uid-2", inUse.Owner)
	assert.Equal(t, []string{"GetRole"}, f.Calls()[before:], "a refused role gets no change")
}

func TestEnsureRoleTakesARoleOfItsRequestWithoutAUID(t *testing.T) {
	// A role that the plugin made before it wrote the UID tag.
	f := awsfake.NewIAM()
	legacy := roleSpec()
	legacy.Tags = awskit.RequestTags("team-a", "orders", "orders-identity")
	_, _, err := awskit.EnsureRole(context.Background(), f, legacy)
	require.NoError(t, err)

	_, change, err := awskit.EnsureRole(context.Background(), f, roleSpec())

	require.NoError(t, err)
	assert.Equal(t, []string{"tags"}, change.Updated)
	got, _ := f.Role(roleSpec().Name)
	assert.Equal(t, "uid-1", got.Tags[awskit.TagRequestUID])
}

func TestEnsureRoleRefusesARoleWithoutAUIDOfAnotherRequest(t *testing.T) {
	for _, tc := range []struct {
		name  string
		tags  map[string]string
		owner string
	}{
		{"another request", awskit.RequestTags("team-b", "orders", "orders-identity"), "the request team-b/orders-identity"},
		{"no request", map[string]string{"owner": "admin"}, "no request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := awsfake.NewIAM()
			made := roleSpec()
			made.Tags = tc.tags
			_, _, err := awskit.EnsureRole(context.Background(), f, made)
			require.NoError(t, err)

			_, _, err = awskit.EnsureRole(context.Background(), f, roleSpec())

			var inUse awskit.RoleInUseError
			require.ErrorAs(t, err, &inUse)
			assert.Equal(t, tc.owner, inUse.Owner)
			got, _ := f.Role(roleSpec().Name)
			assert.Equal(t, tc.tags, got.Tags, "a refused role keeps its tags")
		})
	}
}

func TestRoleInUseErrorNamesTheRoleAndItsOwner(t *testing.T) {
	err := awskit.RoleInUseError{Name: "camunda-0123", Owner: "no request"}

	assert.Equal(
		t,
		"The IAM role camunda-0123 belongs to no request. Delete the role, or change the role name prefix",
		err.Error(),
	)
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

	deleted, err := awskit.DeleteRole(context.Background(), f, roleSpec().Name, "uid-1")

	require.NoError(t, err)
	assert.True(t, deleted)
	assert.Empty(t, f.Roles())
}

func TestDeleteRoleOfAMissingRoleReportsFalse(t *testing.T) {
	deleted, err := awskit.DeleteRole(context.Background(), awsfake.NewIAM(), "gone", "uid-1")

	require.NoError(t, err)
	assert.False(t, deleted)
}

func TestDeleteRoleDeletesOnlyARoleOfTheUID(t *testing.T) {
	f := awsfake.NewIAM()
	_, _, err := awskit.EnsureRole(context.Background(), f, roleSpec())
	require.NoError(t, err)

	for _, uid := range []string{"uid-2", ""} {
		deleted, err := awskit.DeleteRole(context.Background(), f, roleSpec().Name, uid)

		var inUse awskit.RoleInUseError
		require.ErrorAs(t, err, &inUse, uid)
		assert.False(t, deleted, uid)
	}
	_, ok := f.Role(roleSpec().Name)
	assert.True(t, ok, "the role of another UID stays")
	assert.NotContains(t, f.Calls(), "DeleteRolePolicy")

	deleted, err := awskit.DeleteRole(context.Background(), f, roleSpec().Name, "uid-1")
	require.NoError(t, err)
	assert.True(t, deleted)
}

func TestDeleteRoleRefusesARoleWithoutAUID(t *testing.T) {
	f := awsfake.NewIAM()
	made := roleSpec()
	made.Tags = awskit.RequestTags("team-a", "orders", "orders-identity")
	_, _, err := awskit.EnsureRole(context.Background(), f, made)
	require.NoError(t, err)

	deleted, err := awskit.DeleteRole(context.Background(), f, roleSpec().Name, "uid-1")

	var inUse awskit.RoleInUseError
	require.ErrorAs(t, err, &inUse)
	assert.Equal(t, "the request team-a/orders-identity", inUse.Owner)
	assert.False(t, deleted)
}

func TestClaimRoleReturnsARoleOfTheUID(t *testing.T) {
	f := awsfake.NewIAM()
	_, _, err := awskit.EnsureRole(context.Background(), f, roleSpec())
	require.NoError(t, err)
	before := len(f.Calls())

	role, err := awskit.ClaimRole(context.Background(), f, roleSpec().Name, roleSpec().Tags)

	require.NoError(t, err)
	assert.Equal(t, "arn:aws:iam::111122223333:role/"+roleSpec().Name, role.ARN)
	assert.Equal(t, []string{"GetRole"}, f.Calls()[before:])
}

func TestClaimRoleTakesARoleOfItsRequestWithoutAUID(t *testing.T) {
	f := awsfake.NewIAM()
	legacy := roleSpec()
	legacy.Tags = awskit.RequestTags("team-a", "orders", "orders-identity")
	_, _, err := awskit.EnsureRole(context.Background(), f, legacy)
	require.NoError(t, err)

	role, err := awskit.ClaimRole(context.Background(), f, roleSpec().Name, roleSpec().Tags)

	require.NoError(t, err)
	assert.Equal(t, roleSpec().Name, role.Name)
	got, _ := f.Role(roleSpec().Name)
	assert.Equal(t, "uid-1", got.Tags[awskit.TagRequestUID])
}

func TestClaimRoleRefusesARoleOfAnotherRequest(t *testing.T) {
	for name, tags := range map[string]map[string]string{
		"another UID":                 awskit.ExtraTags(nil).OwnerTags("team-a", "orders", "orders-identity", "uid-2"),
		"no UID, and another request": awskit.RequestTags("team-b", "orders", "orders-identity"),
	} {
		t.Run(name, func(t *testing.T) {
			f := awsfake.NewIAM()
			made := roleSpec()
			made.Tags = tags
			_, _, err := awskit.EnsureRole(context.Background(), f, made)
			require.NoError(t, err)

			_, err = awskit.ClaimRole(context.Background(), f, roleSpec().Name, roleSpec().Tags)

			var inUse awskit.RoleInUseError
			require.ErrorAs(t, err, &inUse)
			got, _ := f.Role(roleSpec().Name)
			assert.Equal(t, tags, got.Tags, "a refused role keeps its tags")
		})
	}
}

func TestClaimRoleOfAMissingRoleIsZero(t *testing.T) {
	role, err := awskit.ClaimRole(context.Background(), awsfake.NewIAM(), "gone", roleSpec().Tags)

	require.NoError(t, err)
	assert.Equal(t, awskit.Role{}, role)
}

// staleIAM answers the GetRole after a TagRole once with the role as it was
// before the TagRole, as IAM can while a change propagates.
type staleIAM struct {
	*awsfake.IAM
	stale *iam.GetRoleOutput
}

func (s *staleIAM) TagRole(
	ctx context.Context,
	in *iam.TagRoleInput,
	opts ...func(*iam.Options),
) (*iam.TagRoleOutput, error) {
	before, err := s.IAM.GetRole(ctx, &iam.GetRoleInput{RoleName: in.RoleName})
	if err == nil {
		s.stale = before
	}
	return s.IAM.TagRole(ctx, in, opts...)
}

func (s *staleIAM) GetRole(
	ctx context.Context,
	in *iam.GetRoleInput,
	opts ...func(*iam.Options),
) (*iam.GetRoleOutput, error) {
	if out := s.stale; out != nil {
		s.stale = nil
		return out, nil
	}
	return s.IAM.GetRole(ctx, in, opts...)
}

func TestDeleteClaimedRoleDeletesARoleOfItsRequestWithoutAUID(t *testing.T) {
	f := awsfake.NewIAM()
	legacy := roleSpec()
	legacy.Tags = awskit.RequestTags("team-a", "orders", "orders-identity")
	_, _, err := awskit.EnsureRole(context.Background(), f, legacy)
	require.NoError(t, err)

	deleted, err := awskit.DeleteClaimedRole(context.Background(), f, roleSpec().Name, roleSpec().Tags)

	require.NoError(t, err)
	assert.True(t, deleted)
	assert.Empty(t, f.Roles())
}

func TestDeleteClaimedRoleReportsAUIDTagThatIAMDoesNotShowYet(t *testing.T) {
	f := &staleIAM{IAM: awsfake.NewIAM()}
	legacy := roleSpec()
	legacy.Tags = awskit.RequestTags("team-a", "orders", "orders-identity")
	_, _, err := awskit.EnsureRole(context.Background(), f, legacy)
	require.NoError(t, err)

	deleted, err := awskit.DeleteClaimedRole(context.Background(), f, roleSpec().Name, roleSpec().Tags)

	assert.ErrorIs(t, err, awskit.ErrTagNotVisible)
	assert.False(t, deleted)
	got, ok := f.Role(roleSpec().Name)
	require.True(t, ok, "a later call deletes the role")
	assert.Equal(t, "uid-1", got.Tags[awskit.TagRequestUID])
}

func TestDeleteClaimedRoleKeepsARoleOfAnotherRequest(t *testing.T) {
	f := awsfake.NewIAM()
	other := roleSpec()
	other.Tags = awskit.ExtraTags(nil).OwnerTags("team-a", "orders", "orders-identity", "uid-2")
	_, _, err := awskit.EnsureRole(context.Background(), f, other)
	require.NoError(t, err)

	deleted, err := awskit.DeleteClaimedRole(context.Background(), f, roleSpec().Name, roleSpec().Tags)

	var inUse awskit.RoleInUseError
	require.ErrorAs(t, err, &inUse)
	assert.False(t, deleted)
	assert.Len(t, f.Roles(), 1)
}

func TestDeleteRoleNamesTheInstanceProfileThatUsesTheRole(t *testing.T) {
	f := awsfake.NewIAM()
	_, _, err := awskit.EnsureRole(context.Background(), f, roleSpec())
	require.NoError(t, err)
	f.AddRoleToInstanceProfile(roleSpec().Name, "nodes")

	deleted, err := awskit.DeleteRole(context.Background(), f, roleSpec().Name, "uid-1")

	require.Error(t, err)
	assert.False(t, deleted)
	assert.Contains(t, err.Error(), roleSpec().Name)
	assert.Contains(t, err.Error(), "an instance profile uses it")
}

func TestReplacedRolesSkipsTheCurrentNameInAnyCase(t *testing.T) {
	got := awskit.ReplacedRoles("team-b-0123", []string{"camunda-0123", "TEAM-B-0123", "Camunda-0123", "camunda-4567"})

	assert.Equal(t, []string{"camunda-0123", "camunda-4567"}, got)
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

const (
	policyA = "arn:aws:iam::111122223333:policy/a"
	policyB = "arn:aws:iam::111122223333:policy/b"
	policyC = "arn:aws:iam::111122223333:policy/c"
)

// newRoleWith returns a fake with the role of roleSpec, the policies a, b,
// and c, and attached on the role.
func newRoleWith(t *testing.T, attached ...string) *awsfake.IAM {
	f := awsfake.NewIAM()
	for _, p := range []string{policyA, policyB, policyC} {
		f.AddPolicy(p)
	}
	_, _, err := awskit.EnsureRole(context.Background(), f, roleSpec())
	require.NoError(t, err)
	for _, p := range attached {
		f.AttachPolicy(roleSpec().Name, p)
	}
	return f
}

func TestEnsureAttachedPoliciesAttachesTheMissingAndDetachesTheExtra(t *testing.T) {
	f := newRoleWith(t, policyA, policyC)

	change, err := awskit.EnsureAttachedPolicies(
		context.Background(),
		f,
		roleSpec().Name,
		[]string{policyB, policyA, policyB},
	)

	require.NoError(t, err)
	assert.Equal(t, awskit.PolicyChange{Attached: []string{policyB}, Detached: []string{policyC}}, change)
	role, _ := f.Role(roleSpec().Name)
	assert.ElementsMatch(t, []string{policyA, policyB}, role.Attached)
}

func TestEnsureAttachedPoliciesLeavesARoleInSyncAlone(t *testing.T) {
	f := newRoleWith(t, policyA, policyB)
	before := len(f.Calls())

	change, err := awskit.EnsureAttachedPolicies(context.Background(), f, roleSpec().Name, []string{policyB, policyA})

	require.NoError(t, err)
	assert.Zero(t, change)
	assert.Equal(t, []string{"ListAttachedRolePolicies"}, f.Calls()[before:])
}

// IAM allows 10 managed policies on a role by default, so a swap at the
// quota must free a place first.
func TestEnsureAttachedPoliciesDetachesBeforeItAttaches(t *testing.T) {
	f := newRoleWith(t, policyA, policyB)
	before := len(f.Calls())

	_, err := awskit.EnsureAttachedPolicies(context.Background(), f, roleSpec().Name, []string{policyC})

	require.NoError(t, err)
	assert.Equal(
		t,
		[]string{"ListAttachedRolePolicies", "DetachRolePolicy", "DetachRolePolicy", "AttachRolePolicy"},
		f.Calls()[before:],
	)
}

func TestEnsureAttachedPoliciesReadsEveryPage(t *testing.T) {
	f := newRoleWith(t, policyA, policyB, policyC)
	f.PageSize = 1

	change, err := awskit.EnsureAttachedPolicies(context.Background(), f, roleSpec().Name, nil)

	require.NoError(t, err)
	assert.Equal(t, []string{policyA, policyB, policyC}, change.Detached)
	role, _ := f.Role(roleSpec().Name)
	assert.Empty(t, role.Attached)
}

func TestEnsureAttachedPoliciesKeepsTheAWSErrorCode(t *testing.T) {
	f := newRoleWith(t)
	f.FailNext("AttachRolePolicy", &smithy.GenericAPIError{Code: "LimitExceeded"})

	_, err := awskit.EnsureAttachedPolicies(context.Background(), f, roleSpec().Name, []string{policyA})

	assert.True(t, awskit.IsLimitExceeded(err))
	assert.ErrorContains(t, err, policyA)
}
