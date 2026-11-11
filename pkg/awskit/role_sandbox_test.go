//go:build sandbox

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

// The tests of this file call real IAM in the AWS sandbox account, with the
// credentials of the environment, for what the fake of IAM cannot show. They
// need RUN_ID and AWS_ACCOUNT_ID. Each role and policy starts with RUN_ID and
// has the tag cpo-ci-run=RUN_ID, so the sweep of the run removes what a
// failed test leaves.

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
)

// sandboxTrust is a trust policy that no principal can use.
const sandboxTrust = `{"Version":"2012-10-17","Statement":[{"Effect":"Deny",` +
	`"Principal":{"Service":"pods.eks.amazonaws.com"},"Action":"sts:AssumeRole"}]}`

// sandbox is the account of a test: the IAM client, the start of each name,
// the extra tags with cpo-ci-run, and the boundary of each role.
type sandbox struct {
	iam      *iam.Client
	prefix   string
	extra    awskit.ExtraTags
	boundary string
}

func newSandbox(t *testing.T) sandbox {
	t.Helper()
	run, account := os.Getenv("RUN_ID"), os.Getenv("AWS_ACCOUNT_ID")
	require.NotEmpty(t, run, "RUN_ID is not set")
	require.NotEmpty(t, account, "AWS_ACCOUNT_ID is not set")

	cfg, err := awskit.LoadConfig(context.Background(), os.Getenv("AWS_REGION"))
	require.NoError(t, err)

	return sandbox{
		iam:      iam.NewFromConfig(cfg),
		prefix:   run + "-awskit",
		extra:    awskit.ExtraTags{"cpo-ci-run": run},
		boundary: "arn:aws:iam::" + account + ":policy/cpo-sandbox-boundary",
	}
}

// role returns the spec of the role <prefix>-<name> of the request
// team-a/orders-identity, with the UID uid, or with no UID tag when uid is
// empty. It deletes the role at the end of the test.
func (s sandbox) role(t *testing.T, name, uid string) awskit.RoleSpec {
	t.Helper()
	tags := s.extra.RequestTags("team-a", "orders", "orders-identity")
	if uid != "" {
		tags = s.extra.OwnerTags("team-a", "orders", "orders-identity", uid)
	}

	spec := awskit.RoleSpec{
		Name:                s.prefix + "-" + name,
		TrustPolicy:         []byte(sandboxTrust),
		PermissionsBoundary: s.boundary,
		Tags:                tags,
	}
	t.Cleanup(func() { s.deleteRole(t, spec.Name) })
	return spec
}

// deleteRole deletes the role name, whatever its UID tag, and logs a failure.
func (s sandbox) deleteRole(t *testing.T, name string) {
	ctx := context.Background()
	out, err := s.iam.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(name)})
	if awskit.IsNotFound(err) {
		return
	}

	uid := ""
	if err == nil {
		uid = tagValue(out.Role.Tags, awskit.TagRequestUID)
		_, err = awskit.DeleteRole(ctx, s.iam, name, uid)
	}
	if err != nil {
		t.Logf("the sweep of the run removes the role %s: %v", name, err)
	}
}

func tagValue(tags []types.Tag, key string) string {
	for _, tag := range tags {
		if aws.ToString(tag.Key) == key {
			return aws.ToString(tag.Value)
		}
	}
	return ""
}

func TestSandboxEnsureRoleCreatesTheRoleWithTheRequestUID(t *testing.T) {
	s := newSandbox(t)
	ctx := context.Background()
	spec := s.role(t, "uid", "uid-1")

	_, change, err := awskit.EnsureRole(ctx, s.iam, spec)

	require.NoError(t, err)
	assert.True(t, change.Created)
	out, err := s.iam.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(spec.Name)})
	require.NoError(t, err)
	assert.Equal(t, "uid-1", tagValue(out.Role.Tags, awskit.TagRequestUID))
}

func TestSandboxRoleNamesAreOneNameInAnyCase(t *testing.T) {
	s := newSandbox(t)
	ctx := context.Background()
	spec := s.role(t, "case", "uid-1")
	_, _, err := awskit.EnsureRole(ctx, s.iam, spec)
	require.NoError(t, err)
	// The prefix keeps its case, so that the name stays inside cpo-ci-*.
	other := s.prefix + "-CASE"

	out, err := s.iam.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(other)})
	require.NoError(t, err)
	assert.Equal(t, spec.Name, aws.ToString(out.Role.RoleName))

	_, err = s.iam.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName:                 aws.String(other),
		AssumeRolePolicyDocument: aws.String(sandboxTrust),
		PermissionsBoundary:      aws.String(s.boundary),
		Tags: []types.Tag{
			{Key: aws.String("cpo-ci-run"), Value: aws.String(s.extra["cpo-ci-run"])},
		},
	})
	if err == nil {
		t.Cleanup(func() { s.deleteRole(t, other) })
	}
	assert.True(t, awskit.IsAlreadyExists(err), "CreateRole of %s: %v", other, err)
}

// IAM is eventually consistent, so a read right after ClaimRole can miss the
// UID tag that ClaimRole wrote. DeleteClaimedRole must then report
// ErrTagNotVisible, and a later call must delete the role.
func TestSandboxDeleteClaimedRoleDeletesARoleThatItClaimed(t *testing.T) {
	s := newSandbox(t)
	ctx := context.Background()
	owner := s.extra.OwnerTags("team-a", "orders", "orders-identity", "uid-1")

	var stale int
	var longest time.Duration
	for i := range 10 {
		spec := s.role(t, fmt.Sprintf("claim-%d", i), "")
		_, _, err := awskit.EnsureRole(ctx, s.iam, spec)
		require.NoError(t, err)

		start := time.Now()
		deleted, err := awskit.DeleteClaimedRole(ctx, s.iam, spec.Name, owner)
		for errors.Is(err, awskit.ErrTagNotVisible) && time.Since(start) < time.Minute {
			stale++
			time.Sleep(200 * time.Millisecond)
			deleted, err = awskit.DeleteClaimedRole(ctx, s.iam, spec.Name, owner)
		}
		longest = max(longest, time.Since(start))

		require.NoError(t, err, spec.Name)
		assert.True(t, deleted, spec.Name)
	}
	t.Logf("10 roles, %d reads without the new UID tag, the longest delete took %s", stale, longest)
}

func TestSandboxDeleteRoleRemovesTheManagedAndInlinePoliciesFirst(t *testing.T) {
	s := newSandbox(t)
	ctx := context.Background()
	spec := s.role(t, "policies", "uid-1")
	_, _, err := awskit.EnsureRole(ctx, s.iam, spec)
	require.NoError(t, err)
	pol, err := s.iam.CreatePolicy(ctx, &iam.CreatePolicyInput{
		PolicyName: aws.String(s.prefix + "-policies"),
		PolicyDocument: aws.String(
			`{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":"s3:GetObject","Resource":"*"}]}`,
		),
		Tags: []types.Tag{{Key: aws.String("cpo-ci-run"), Value: aws.String(s.extra["cpo-ci-run"])}},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		if _, err := s.iam.DeletePolicy(ctx, &iam.DeletePolicyInput{PolicyArn: pol.Policy.Arn}); err != nil {
			t.Logf("the sweep of the run removes the policy %s: %v", aws.ToString(pol.Policy.Arn), err)
		}
	})
	_, err = s.iam.AttachRolePolicy(ctx, &iam.AttachRolePolicyInput{
		RoleName:  aws.String(spec.Name),
		PolicyArn: pol.Policy.Arn,
	})
	require.NoError(t, err)
	require.NoError(t, awskit.PutInlinePolicy(ctx, s.iam, spec.Name, "inline", []byte(
		`{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":"s3:PutObject","Resource":"*"}]}`,
	)))

	deleted, err := awskit.DeleteRole(ctx, s.iam, spec.Name, "uid-1")

	require.NoError(t, err)
	assert.True(t, deleted)
	_, err = s.iam.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(spec.Name)})
	assert.True(t, awskit.IsNotFound(err), "GetRole after the delete: %v", err)
}

func TestSandboxClaimRoleOfARoleWithFiftyTagsIsLimitExceeded(t *testing.T) {
	s := newSandbox(t)
	ctx := context.Background()
	spec := s.role(t, "full", "")
	for i := len(spec.Tags); i < 50; i++ {
		spec.Tags[fmt.Sprintf("k%02d", i)] = "v"
	}
	_, _, err := awskit.EnsureRole(ctx, s.iam, spec)
	require.NoError(t, err)
	tags := maps.Clone(spec.Tags)
	tags[awskit.TagRequestUID] = "uid-1"

	_, err = awskit.ClaimRole(ctx, s.iam, spec.Name, tags)

	assert.True(t, awskit.IsLimitExceeded(err), "ClaimRole with 51 tags: %v", err)
}
