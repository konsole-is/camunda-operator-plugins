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
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
)

func associate(f *EKS, sa, role string) (*eks.CreatePodIdentityAssociationOutput, error) {
	return f.CreatePodIdentityAssociation(context.Background(), &eks.CreatePodIdentityAssociationInput{
		ClusterName:    aws.String("eks"),
		Namespace:      aws.String("team-a"),
		ServiceAccount: aws.String(sa),
		RoleArn:        aws.String(role),
		Tags:           map[string]string{"k": "v"},
	})
}

func TestEKSRefusesASecondAssociationForAServiceAccount(t *testing.T) {
	f := NewEKS()
	_, err := associate(f, "orders-camunda", "arn:aws:iam::1:role/a")
	require.NoError(t, err)

	_, err = associate(f, "orders-camunda", "arn:aws:iam::1:role/b")

	assert.True(t, awskit.IsAlreadyExists(err), err)
	assert.Len(t, f.Associations(), 1)
}

func TestEKSDescribesAnAssociationWithItsRoleAndTags(t *testing.T) {
	f := NewEKS()
	out, err := associate(f, "orders-camunda", "arn:aws:iam::1:role/a")
	require.NoError(t, err)

	got, err := f.DescribePodIdentityAssociation(context.Background(), &eks.DescribePodIdentityAssociationInput{
		ClusterName:   aws.String("eks"),
		AssociationId: out.Association.AssociationId,
	})

	require.NoError(t, err)
	assert.Equal(t, "arn:aws:iam::1:role/a", aws.ToString(got.Association.RoleArn))
	assert.Equal(t, map[string]string{"k": "v"}, got.Association.Tags)

	_, err = f.DescribePodIdentityAssociation(context.Background(), &eks.DescribePodIdentityAssociationInput{
		ClusterName:   aws.String("eks"),
		AssociationId: aws.String("gone"),
	})
	assert.True(t, awskit.IsNotFound(err), err)
}

func TestEKSPagesAssociations(t *testing.T) {
	f := NewEKS()
	f.PageSize = 1
	for _, sa := range []string{"a", "b", "c"} {
		_, err := associate(f, sa, "arn:aws:iam::1:role/a")
		require.NoError(t, err)
	}

	var sas []string
	var token *string
	pages := 0
	for {
		out, err := f.ListPodIdentityAssociations(context.Background(), &eks.ListPodIdentityAssociationsInput{
			ClusterName: aws.String("eks"),
			Namespace:   aws.String("team-a"),
			NextToken:   token,
		})
		require.NoError(t, err)
		pages++
		for _, a := range out.Associations {
			sas = append(sas, aws.ToString(a.ServiceAccount))
		}
		if out.NextToken == nil {
			break
		}
		token = out.NextToken
	}

	assert.Equal(t, []string{"a", "b", "c"}, sas)
	assert.Equal(t, 3, pages)
}
