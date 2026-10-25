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
	"testing"

	"github.com/stretchr/testify/assert"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
)

func TestSummaryListsTheRoleTheGrantsTheSizeAndTheBoundary(t *testing.T) {
	role := awskit.Role{Name: "camunda-0123456789abcdef", ARN: "arn:aws:iam::1:role/camunda-0123456789abcdef"}

	got := summary(role, "arn:aws:iam::1:policy/b", 2, 3, 8192)

	assert.Equal(t, []pluginsv1alpha1.SummaryItem{
		{Label: "Role", Value: "camunda-0123456789abcdef"},
		{Label: "Applied grants", Value: "2 of 3"},
		{Label: "Policy size", Value: "8192 of 10240 characters", Severity: pluginsv1alpha1.SeverityInfo},
		{Label: "Permissions boundary", Value: "arn:aws:iam::1:policy/b"},
	}, got)
}

func TestSummaryWarnsAbove80Percent(t *testing.T) {
	got := summary(awskit.Role{Name: "r"}, "b", 0, 0, 8193)

	assert.Equal(t, pluginsv1alpha1.SeverityWarning, got[2].Severity)
}
