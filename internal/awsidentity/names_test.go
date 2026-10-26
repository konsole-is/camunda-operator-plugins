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

package awsidentity

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/util/validation"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
)

func TestServiceAccountsJoinsTheTargetNames(t *testing.T) {
	got := serviceAccounts(pluginsv1alpha1.TargetRef{
		ServiceAccountName:        "orders-camunda",
		AdditionalServiceAccounts: []string{"orders-pg-postgres", "orders-es-es", "orders-camunda", ""},
	})

	assert.Equal(t, []string{"orders-camunda", "orders-es-es", "orders-pg-postgres"}, got)
}

func TestServiceAccountsOfATargetWithoutNamesIsEmpty(t *testing.T) {
	assert.Empty(t, serviceAccounts(pluginsv1alpha1.TargetRef{}))
}

func TestPolicyNameFitsIAM(t *testing.T) {
	assert.Equal(
		t,
		"grant-orders-backup-storage-bucket-read-write-1a2b3c4d",
		policyName("orders-backup-storage-bucket-read-write-1a2b3c4d"),
	)
	assert.Len(t, policyName(strings.Repeat("g", 253)), 128)
}

func TestPolicyNameOfALongGrantIsNoGrantName(t *testing.T) {
	long := strings.Repeat("a", 200)
	cut := strings.TrimPrefix(policyName(long), policyPrefix)

	assert.Len(t, policyName(long), 128)
	assert.NotEmpty(t, validation.IsDNS1123Subdomain(cut), "a Grant cannot have the name %s", cut)
	assert.NotEqual(t, policyName(long), policyName(strings.Repeat("a", 201)))
}
