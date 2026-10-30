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
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
)

func TestIdentityOutputs(t *testing.T) {
	const arn = "arn:aws:iam::111122223333:role/x"

	out := awskit.IdentityOutputs(awskit.ModeIRSA, arn)
	assert.Equal(t, arn, out[awskit.OutputPrincipal])
	assert.JSONEq(t, `{"eks.amazonaws.com/role-arn":"`+arn+`"}`, out[awskit.OutputServiceAccountAnnotations])

	out = awskit.IdentityOutputs(awskit.ModePodIdentity, arn)
	assert.Equal(t, arn, out[awskit.OutputPrincipal])
	assert.JSONEq(t, `{}`, out[awskit.OutputServiceAccountAnnotations])
	assert.Len(t, out, 2)
}
