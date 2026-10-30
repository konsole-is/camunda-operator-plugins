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
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit/awsfake"
)

const (
	roleA = "arn:aws:iam::111122223333:role/camunda-a"
	roleB = "arn:aws:iam::111122223333:role/camunda-b"
)

func hubTags() map[string]string {
	return awskit.RequestTags("team-a", "orders", "orders-identity")
}

func ensure(t *testing.T, f *awsfake.EKS, role string, sas ...string) awskit.AssociationChange {
	_, change, err := awskit.EnsureAssociations(context.Background(), f, "eks", "team-a", sas, role, hubTags())
	require.NoError(t, err)
	return change
}

func TestEnsureAssociationsCreatesOneForEachServiceAccount(t *testing.T) {
	f := awsfake.NewEKS()

	got, change, err := awskit.EnsureAssociations(
		context.Background(),
		f,
		"eks",
		"team-a",
		[]string{"orders-camunda", "orders-es-es"},
		roleA,
		hubTags(),
	)

	require.NoError(t, err)
	assert.Equal(t, []string{"orders-camunda", "orders-es-es"}, change.Created)
	require.Len(t, got, 2)
	assert.Equal(t, "orders-camunda", got[0].ServiceAccount)
	assert.Equal(t, roleA, got[0].RoleARN)
	assert.NotEmpty(t, got[0].ID)
	for _, a := range f.Associations() {
		assert.Equal(t, roleA, a.RoleARN)
		assert.Equal(t, "team-a", a.Namespace)
		assert.Equal(t, hubTags(), a.Tags)
	}
}

func TestEnsureAssociationsIsStable(t *testing.T) {
	f := awsfake.NewEKS()
	ensure(t, f, roleA, "orders-camunda", "orders-es-es")
	before := len(f.Calls())

	change := ensure(t, f, roleA, "orders-camunda", "orders-es-es")

	assert.Equal(t, awskit.AssociationChange{}, change)
	assert.NotContains(t, f.Calls()[before:], "CreatePodIdentityAssociation")
	assert.Len(t, f.Associations(), 2)
}

func TestEnsureAssociationsDeletesAnUnlistedServiceAccount(t *testing.T) {
	f := awsfake.NewEKS()
	ensure(t, f, roleA, "orders-camunda", "orders-es-es")

	change := ensure(t, f, roleA, "orders-camunda")

	assert.Equal(t, []string{"orders-es-es"}, change.Deleted)
	require.Len(t, f.Associations(), 1)
	assert.Equal(t, "orders-camunda", f.Associations()[0].ServiceAccount)
}

func TestEnsureAssociationsMovesItsOwnAssociation(t *testing.T) {
	f := awsfake.NewEKS()
	ensure(t, f, roleB, "orders-camunda")

	change := ensure(t, f, roleA, "orders-camunda")

	assert.Equal(t, []string{"orders-camunda"}, change.Updated)
	assert.Equal(t, roleA, f.Associations()[0].RoleARN)
}

func TestEnsureAssociationsRefusesAForeignAssociation(t *testing.T) {
	f := awsfake.NewEKS()
	_, _, err := awskit.EnsureAssociations(
		context.Background(),
		f,
		"eks",
		"team-a",
		[]string{"orders-camunda"},
		roleB,
		map[string]string{"owner": "someone"},
	)
	require.NoError(t, err)

	_, _, err = awskit.EnsureAssociations(
		context.Background(),
		f,
		"eks",
		"team-a",
		[]string{"orders-camunda"},
		roleA,
		hubTags(),
	)

	require.Error(t, err)
	assert.ErrorContains(t, err, "orders-camunda")
	assert.ErrorContains(t, err, roleB)
	assert.Equal(t, roleB, f.Associations()[0].RoleARN)
}

func TestEnsureAssociationsLeavesTheAssociationsOfAnotherRoleAlone(t *testing.T) {
	f := awsfake.NewEKS()
	_, _, err := awskit.EnsureAssociations(
		context.Background(),
		f,
		"eks",
		"team-a",
		[]string{"billing-camunda"},
		roleB,
		awskit.RequestTags("team-a", "billing", "billing-identity"),
	)
	require.NoError(t, err)

	change := ensure(t, f, roleA, "orders-camunda")

	assert.Empty(t, change.Deleted)
	assert.Len(t, f.Associations(), 2)
}

func TestEnsureAssociationsDeletesAnUnlistedServiceAccountWhenAnotherIsRefused(t *testing.T) {
	f := awsfake.NewEKS()
	ensure(t, f, roleA, "old-sa")
	_, _, err := awskit.EnsureAssociations(
		context.Background(),
		f,
		"eks",
		"team-a",
		[]string{"taken"},
		roleB,
		awskit.RequestTags("team-a", "billing", "billing-identity"),
	)
	require.NoError(t, err)

	_, _, err = awskit.EnsureAssociations(
		context.Background(),
		f,
		"eks",
		"team-a",
		[]string{"taken"},
		roleA,
		hubTags(),
	)

	require.ErrorContains(t, err, "taken")
	for _, a := range f.Associations() {
		assert.NotEqual(t, "old-sa", a.ServiceAccount)
	}
}

func TestEnsureAssociationsReadsEveryPage(t *testing.T) {
	f := awsfake.NewEKS()
	f.PageSize = 1
	ensure(t, f, roleA, "a", "b", "c")

	change := ensure(t, f, roleA, "a", "b", "c")

	assert.Equal(t, awskit.AssociationChange{}, change)
	assert.Len(t, f.Associations(), 3)
}

func TestDeleteAssociationsKeepsOtherRoles(t *testing.T) {
	f := awsfake.NewEKS()
	f.PageSize = 1
	ensure(t, f, roleA, "orders-camunda", "orders-es-es")
	_, _, err := awskit.EnsureAssociations(
		context.Background(),
		f,
		"eks",
		"team-a",
		[]string{"billing-camunda"},
		roleB,
		awskit.RequestTags("team-a", "billing", "billing-identity"),
	)
	require.NoError(t, err)

	deleted, err := awskit.DeleteAssociations(context.Background(), f, "eks", "team-a", roleA, hubTags())

	require.NoError(t, err)
	assert.Equal(t, []string{"orders-camunda", "orders-es-es"}, deleted)
	require.Len(t, f.Associations(), 1)
	assert.Equal(t, roleB, f.Associations()[0].RoleARN)
}

func billingTags() map[string]string {
	return awskit.RequestTags("team-a", "billing", "billing-connectors")
}

// Two claims of one namespace can share one role that an admin made.
func TestEnsureAssociationsKeepsTheAssociationOfAnotherClaimOfTheRole(t *testing.T) {
	f := awsfake.NewEKS()
	ensure(t, f, roleA, "orders-connectors")

	_, change, err := awskit.EnsureAssociations(
		context.Background(),
		f,
		"eks",
		"team-a",
		[]string{"billing-connectors"},
		roleA,
		billingTags(),
	)
	require.NoError(t, err)
	assert.Empty(t, change.Deleted)

	change = ensure(t, f, roleA, "orders-connectors")

	assert.Equal(t, awskit.AssociationChange{}, change)
	var sas []string
	for _, a := range f.Associations() {
		sas = append(sas, a.ServiceAccount)
	}
	assert.ElementsMatch(t, []string{"orders-connectors", "billing-connectors"}, sas)
}

func TestEnsureAssociationsKeepsAnAssociationOfItsRoleThatCarriesNoClaimTags(t *testing.T) {
	f := awsfake.NewEKS()
	_, err := f.CreatePodIdentityAssociation(context.Background(), &eks.CreatePodIdentityAssociationInput{
		ClusterName:    aws.String("eks"),
		Namespace:      aws.String("team-a"),
		ServiceAccount: aws.String("by-hand"),
		RoleArn:        aws.String(roleA),
	})
	require.NoError(t, err)

	change := ensure(t, f, roleA, "orders-camunda")

	assert.Empty(t, change.Deleted)
	assert.Len(t, f.Associations(), 2)
}

func TestDeleteAssociationsDeletesOnlyTheAssociationsOfTheClaim(t *testing.T) {
	f := awsfake.NewEKS()
	ensure(t, f, roleA, "orders-connectors")
	_, _, err := awskit.EnsureAssociations(
		context.Background(),
		f,
		"eks",
		"team-a",
		[]string{"billing-connectors"},
		roleA,
		billingTags(),
	)
	require.NoError(t, err)

	deleted, err := awskit.DeleteAssociations(context.Background(), f, "eks", "team-a", roleA, hubTags())

	require.NoError(t, err)
	assert.Equal(t, []string{"orders-connectors"}, deleted)
	require.Len(t, f.Associations(), 1)
	assert.Equal(t, "billing-connectors", f.Associations()[0].ServiceAccount)
}
