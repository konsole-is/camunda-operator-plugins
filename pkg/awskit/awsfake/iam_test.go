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

package awsfake

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
)

const trust = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sts:AssumeRole"}]}`

// createRole creates the role r.
func createRole(t *testing.T, f *IAM) {
	_, err := f.CreateRole(context.Background(), &iam.CreateRoleInput{
		RoleName:                 aws.String("r"),
		AssumeRolePolicyDocument: aws.String(trust),
	})
	require.NoError(t, err)
}

// putPolicy puts the inline policy name on the role r.
func putPolicy(f *IAM, name, doc string) error {
	_, err := f.PutRolePolicy(context.Background(), &iam.PutRolePolicyInput{
		RoleName:       aws.String("r"),
		PolicyName:     aws.String(name),
		PolicyDocument: aws.String(doc),
	})
	return err
}

// policyOfSize returns a JSON document of n characters.
func policyOfSize(n int) string {
	return `{"a":"` + strings.Repeat("x", n-8) + `"}`
}

func TestIAMReturnsDocumentsURLEncoded(t *testing.T) {
	f := NewIAM()
	createRole(t, f)
	require.NoError(t, putPolicy(f, "p", `{"Resource":"a b"}`))

	role, err := f.GetRole(context.Background(), &iam.GetRoleInput{RoleName: aws.String("r")})
	require.NoError(t, err)
	policy, err := f.GetRolePolicy(
		context.Background(),
		&iam.GetRolePolicyInput{RoleName: aws.String("r"), PolicyName: aws.String("p")},
	)
	require.NoError(t, err)

	assert.Equal(t, url.QueryEscape(trust), aws.ToString(role.Role.AssumeRolePolicyDocument))
	assert.Equal(t, "arn:aws:iam::111122223333:role/r", aws.ToString(role.Role.Arn))
	assert.Equal(t, url.QueryEscape(`{"Resource":"a b"}`), aws.ToString(policy.PolicyDocument))
}

func TestIAMRefusesInlinePoliciesOverTheLimit(t *testing.T) {
	f := NewIAM()
	createRole(t, f)

	require.NoError(t, putPolicy(f, "a", policyOfSize(6000)))
	err := putPolicy(f, "b", policyOfSize(5000))

	assert.True(t, awskit.IsLimitExceeded(err), err)
	role, _ := f.Role("r")
	assert.Len(t, role.InlinePolicies, 1)
	assert.NoError(t, putPolicy(f, "a", policyOfSize(7000)), "a replaced policy counts once")
}

func TestIAMRefusesAPolicyThatIsNotJSON(t *testing.T) {
	f := NewIAM()
	createRole(t, f)

	err := putPolicy(f, "a", `{`)

	assert.True(t, awskit.IsMalformedPolicy(err), err)
}

func TestIAMPagesRolePolicies(t *testing.T) {
	f := NewIAM()
	f.PageSize = 1
	createRole(t, f)
	for _, name := range []string{"a", "b", "c"} {
		require.NoError(t, putPolicy(f, name, `{}`))
	}

	var names []string
	var marker *string
	pages := 0
	for {
		out, err := f.ListRolePolicies(
			context.Background(),
			&iam.ListRolePoliciesInput{RoleName: aws.String("r"), Marker: marker},
		)
		require.NoError(t, err)
		pages++
		names = append(names, out.PolicyNames...)
		if !out.IsTruncated {
			break
		}
		marker = out.Marker
	}

	assert.Equal(t, []string{"a", "b", "c"}, names)
	assert.Equal(t, 3, pages)
}

func TestIAMRefusesToDeleteARoleWithPolicies(t *testing.T) {
	f := NewIAM()
	createRole(t, f)
	require.NoError(t, putPolicy(f, "a", `{}`))

	_, err := f.DeleteRole(context.Background(), &iam.DeleteRoleInput{RoleName: aws.String("r")})

	assert.Equal(t, "DeleteConflict", awskit.ErrorCode(err))
	_, ok := f.Role("r")
	assert.True(t, ok)
}

func TestIAMRefusesAMissingRoleAndASecondCreate(t *testing.T) {
	f := NewIAM()

	_, err := f.GetRole(context.Background(), &iam.GetRoleInput{RoleName: aws.String("r")})
	assert.True(t, awskit.IsNotFound(err), err)

	createRole(t, f)
	_, err = f.CreateRole(context.Background(), &iam.CreateRoleInput{
		RoleName:                 aws.String("r"),
		AssumeRolePolicyDocument: aws.String(trust),
	})
	assert.True(t, awskit.IsAlreadyExists(err), err)
}

func TestIAMFailNextFailsOneCall(t *testing.T) {
	f := NewIAM()
	createRole(t, f)
	boom := errors.New("boom")
	f.FailNext("GetRole", boom)

	_, first := f.GetRole(context.Background(), &iam.GetRoleInput{RoleName: aws.String("r")})
	_, second := f.GetRole(context.Background(), &iam.GetRoleInput{RoleName: aws.String("r")})

	assert.ErrorIs(t, first, boom)
	assert.NoError(t, second)
	assert.Equal(t, []string{"CreateRole", "GetRole", "GetRole"}, f.Calls())
}
