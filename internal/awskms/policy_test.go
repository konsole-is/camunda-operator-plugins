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

package awskms

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	admin      = "arn:aws:iam::111122223333:role/kms-admin"
	csiDriver  = "arn:aws:iam::111122223333:role/ebs-csi"
	rootPolicy = `{"Sid":"AccountRoot","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::111122223333:root"},` +
		`"Action":["kms:*"],"Resource":"*"}`
	adminPolicy = `{"Sid":"KeyAdministrators","Effect":"Allow","Principal":{"AWS":["` + admin + `"]},` +
		`"Action":["kms:Create*","kms:Describe*","kms:Enable*","kms:List*","kms:Put*","kms:Update*",` +
		`"kms:Revoke*","kms:Disable*","kms:Get*","kms:Delete*","kms:TagResource","kms:UntagResource",` +
		`"kms:ScheduleKeyDeletion","kms:CancelKeyDeletion","kms:ReplicateKey"],"Resource":"*"}`
	csiPolicy = `{"Sid":"EBSCSIDriverUse","Effect":"Allow","Principal":{"AWS":"` + csiDriver + `"},` +
		`"Action":["kms:Encrypt","kms:Decrypt","kms:ReEncrypt*","kms:GenerateDataKey*","kms:DescribeKey"],` +
		`"Resource":"*"},` +
		`{"Sid":"EBSCSIDriverGrants","Effect":"Allow","Principal":{"AWS":"` + csiDriver + `"},` +
		`"Action":["kms:CreateGrant"],"Resource":"*",` +
		`"Condition":{"Bool":{"kms:GrantIsForAWSResource":"true"}}}`
)

func policySettings(admins []string, driver string) Settings {
	return Settings{Partition: "aws", Account: "111122223333", KeyAdministratorARNs: admins, CSIDriverRoleARN: driver}
}

func TestKeyPolicyIsDefaultWithoutPolicyValues(t *testing.T) {
	doc, custom, err := keyPolicy(policySettings(nil, ""))

	require.NoError(t, err)
	assert.False(t, custom)
	assert.Nil(t, doc)
}

func TestKeyPolicyWithAdministratorsAndCSIDriver(t *testing.T) {
	doc, custom, err := keyPolicy(policySettings([]string{admin}, csiDriver))

	require.NoError(t, err)
	assert.True(t, custom)
	assert.JSONEq(t, `{"Version":"2012-10-17","Statement":[`+rootPolicy+`,`+adminPolicy+`,`+csiPolicy+`]}`, string(doc))
}

func TestKeyPolicyWithoutAdministrators(t *testing.T) {
	doc, custom, err := keyPolicy(policySettings(nil, csiDriver))

	require.NoError(t, err)
	assert.True(t, custom)
	assert.JSONEq(t, `{"Version":"2012-10-17","Statement":[`+rootPolicy+`,`+csiPolicy+`]}`, string(doc))
}

func TestKeyPolicyWithoutCSIDriver(t *testing.T) {
	doc, custom, err := keyPolicy(policySettings([]string{admin}, ""))

	require.NoError(t, err)
	assert.True(t, custom)
	assert.JSONEq(t, `{"Version":"2012-10-17","Statement":[`+rootPolicy+`,`+adminPolicy+`]}`, string(doc))
}
