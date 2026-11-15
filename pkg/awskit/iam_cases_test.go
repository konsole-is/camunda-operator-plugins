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
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
)

var iamCases = []awsCase{
	{"returns the trust and inline policies URL-encoded", func(t *testing.T, a account) {
		ctx := context.Background()
		role := a.role(t, "iam-encoded")
		policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":"s3:GetObject","Resource":"*",` +
			`"Condition":{"StringEquals":{"aws:PrincipalTag/team":"a b"}}}]}`
		require.NoError(t, awskit.PutInlinePolicy(ctx, a.iam, role, "p", []byte(policy)))

		a.eventually(t, func(c *assert.CollectT) {
			got, err := a.iam.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(role)})
			require.NoError(c, err)
			trust := aws.ToString(got.Role.AssumeRolePolicyDocument)
			assert.True(c, strings.HasPrefix(trust, "%7B"), "the trust policy as IAM gives it: %s", trust)
			decoded, err := awskit.DecodePolicy(trust)
			require.NoError(c, err)
			assert.Equal(c, sandboxTrust, string(decoded))
			assert.Equal(c, "arn:aws:iam::"+a.id+":role/"+role, aws.ToString(got.Role.Arn))

			inline, err := a.iam.GetRolePolicy(ctx, &iam.GetRolePolicyInput{
				RoleName:   aws.String(role),
				PolicyName: aws.String("p"),
			})
			require.NoError(c, err)
			doc := aws.ToString(inline.PolicyDocument)
			assert.True(c, strings.HasPrefix(doc, "%7B"), "the inline policy as IAM gives it: %s", doc)
			decoded, err = awskit.DecodePolicy(doc)
			require.NoError(c, err)
			assert.Equal(c, policy, string(decoded))
		})
	}},
	{"refuses inline policies over 10,240 characters", func(t *testing.T, a account) {
		ctx := context.Background()
		role := a.role(t, "iam-limit")
		require.NoError(t, awskit.PutInlinePolicy(ctx, a.iam, role, "a", []byte(iamPolicyOfSize(6000))))

		err := awskit.PutInlinePolicy(ctx, a.iam, role, "b", []byte(iamPolicyOfSize(5000)))

		assert.True(t, awskit.IsLimitExceeded(err), "a second policy over the limit: %v", err)
		a.eventually(t, func(c *assert.CollectT) {
			out, err := a.iam.ListRolePolicies(ctx, &iam.ListRolePoliciesInput{RoleName: aws.String(role)})
			require.NoError(c, err)
			assert.Equal(c, []string{"a"}, out.PolicyNames)
		})
		assert.NoError(
			t,
			awskit.PutInlinePolicy(ctx, a.iam, role, "a", []byte(iamPolicyOfSize(7000))),
			"a replaced policy counts once",
		)
	}},
	{"refuses a policy that is not JSON", func(t *testing.T, a account) {
		role := a.role(t, "iam-json")

		err := awskit.PutInlinePolicy(context.Background(), a.iam, role, "a", []byte(`{`))

		assert.True(t, awskit.IsMalformedPolicy(err), "a policy that is not JSON: %v", err)
	}},
	{"pages the inline policies by MaxItems", func(t *testing.T, a account) {
		ctx := context.Background()
		role := a.role(t, "iam-pages")
		for _, name := range []string{"a", "b", "c"} {
			require.NoError(t, awskit.PutInlinePolicy(ctx, a.iam, role, name, []byte(iamPolicyOfSize(200))))
		}

		a.eventually(t, func(c *assert.CollectT) {
			var names []string
			var marker *string
			pages := 0
			for {
				out, err := a.iam.ListRolePolicies(ctx, &iam.ListRolePoliciesInput{
					RoleName: aws.String(role),
					MaxItems: aws.Int32(1),
					Marker:   marker,
				})
				require.NoError(c, err)
				pages++
				names = append(names, out.PolicyNames...)
				if !out.IsTruncated {
					break
				}
				marker = out.Marker
			}

			assert.Equal(c, []string{"a", "b", "c"}, names)
			assert.Equal(c, 3, pages)
		})
	}},
	{"refuses to delete a role with an inline policy", func(t *testing.T, a account) {
		ctx := context.Background()
		role := a.role(t, "iam-conflict")
		require.NoError(t, awskit.PutInlinePolicy(ctx, a.iam, role, "a", []byte(iamPolicyOfSize(200))))

		_, err := a.iam.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String(role)})

		assert.Equal(t, "DeleteConflict", awskit.ErrorCode(err), "the delete of a role with a policy: %v", err)
		_, err = a.iam.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(role)})
		assert.NoError(t, err)
	}},
	{"refuses a missing role and a second create", func(t *testing.T, a account) {
		ctx := context.Background()

		_, err := a.iam.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(a.name("iam-missing"))})
		assert.True(t, awskit.IsNotFound(err), "GetRole of a missing role: %v", err)

		role := a.role(t, "iam-twice")
		_, err = a.iam.CreateRole(ctx, &iam.CreateRoleInput{
			RoleName:                 aws.String(role),
			AssumeRolePolicyDocument: aws.String(sandboxTrust),
			PermissionsBoundary:      aws.String(a.boundary),
			Tags:                     iamTags(a.tags),
		})
		assert.True(t, awskit.IsAlreadyExists(err), "a second CreateRole: %v", err)
	}},
}

// iamPolicyOfSize returns an IAM policy of n characters, without white space.
func iamPolicyOfSize(n int) string {
	head := `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":"s3:GetObject",` +
		`"Resource":"arn:aws:s3:::`
	tail := `"}]}`
	return head + strings.Repeat("x", n-len(head)-len(tail)) + tail
}
