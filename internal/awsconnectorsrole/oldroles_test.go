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
	"testing"

	"github.com/stretchr/testify/assert"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
)

func TestOldRoleRowsKeepsTheIAMRoleRowsOfOtherNames(t *testing.T) {
	rows := []pluginsv1alpha1.ResourceRef{
		{Kind: kindIAMRole, Name: "team-b-0123"},
		{Kind: kindIAMRole, Name: "camunda-0123"},
		{Kind: kindIAMRole, Name: "CAMUNDA-0123"},
		{Kind: kindPodIdentityAssociation, Name: "orders-camunda"},
		{Kind: "Grant", Name: "orders-backup"},
	}

	assert.Equal(
		t,
		[]pluginsv1alpha1.ResourceRef{{Kind: kindIAMRole, Name: "camunda-0123"}},
		oldRoleRows(rows, "team-b-0123"),
	)
	assert.Len(t, oldRoleRows(rows, ""), 2, "no current name keeps each role once")
}
