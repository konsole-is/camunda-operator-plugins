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

func TestIAMFailRoleFailsEachCallOnTheRoleUntilCleared(t *testing.T) {
	f := NewIAM()
	createRole(t, f)
	boom := errors.New("boom")
	f.FailRole("r", boom)

	_, get := f.GetRole(context.Background(), &iam.GetRoleInput{RoleName: aws.String("r")})
	_, tag := f.TagRole(context.Background(), &iam.TagRoleInput{RoleName: aws.String("r")})
	_, other := f.GetRole(context.Background(), &iam.GetRoleInput{RoleName: aws.String("other")})
	f.FailRole("r", nil)
	_, cleared := f.GetRole(context.Background(), &iam.GetRoleInput{RoleName: aws.String("r")})

	assert.ErrorIs(t, get, boom)
	assert.ErrorIs(t, tag, boom)
	assert.True(t, awskit.IsNotFound(other), "another role is not failed")
	assert.NoError(t, cleared)
	assert.Equal(t, []string{"CreateRole", "GetRole", "TagRole", "GetRole", "GetRole"}, f.Calls())
}

func TestIAMFailNextOnRoleFailsOnlyTheNextCallOfTheOperationOnTheRole(t *testing.T) {
	f := NewIAM()
	createRole(t, f)
	boom := errors.New("boom")
	f.FailNextOnRole("r", "GetRole", boom)

	_, other := f.GetRole(context.Background(), &iam.GetRoleInput{RoleName: aws.String("other")})
	_, tag := f.TagRole(context.Background(), &iam.TagRoleInput{RoleName: aws.String("r")})
	_, failed := f.GetRole(context.Background(), &iam.GetRoleInput{RoleName: aws.String("r")})
	_, next := f.GetRole(context.Background(), &iam.GetRoleInput{RoleName: aws.String("r")})

	assert.True(t, awskit.IsNotFound(other), "another role is not failed")
	assert.NoError(t, tag, "another operation on the role is not failed")
	assert.ErrorIs(t, failed, boom)
	assert.NoError(t, next)
	assert.Equal(t, []string{"CreateRole", "GetRole", "TagRole", "GetRole", "GetRole"}, f.Calls())
}

func TestIAMDeleteRoleRefusesARoleInAnInstanceProfileUntilItIsRemoved(t *testing.T) {
	f := NewIAM()
	createRole(t, f)
	f.AddRoleToInstanceProfile("r", "nodes")

	_, refused := f.DeleteRole(context.Background(), &iam.DeleteRoleInput{RoleName: aws.String("r")})
	f.RemoveRoleFromInstanceProfile("r", "nodes")
	_, deleted := f.DeleteRole(context.Background(), &iam.DeleteRoleInput{RoleName: aws.String("r")})

	assert.ErrorContains(t, refused, "instance profile")
	assert.NoError(t, deleted)
}
