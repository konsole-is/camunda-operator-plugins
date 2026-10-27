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
	"fmt"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
)

// summary returns the summary of a request: the role first, then the
// applied grants of total, the size of the inline policies with a Warning
// above 80% of the limit, and the permissions boundary.
func summary(role awskit.Role, boundary string, applied, total, size int) []pluginsv1alpha1.SummaryItem {
	severity := pluginsv1alpha1.SeverityInfo
	if size*100 > 80*awskit.MaxInlinePolicySize {
		severity = pluginsv1alpha1.SeverityWarning
	}

	return []pluginsv1alpha1.SummaryItem{
		{Label: "Role", Value: role.Name},
		{Label: "Applied grants", Value: fmt.Sprintf("%d of %d", applied, total)},
		{
			Label:    "Policy size",
			Value:    fmt.Sprintf("%d of %d characters", size, awskit.MaxInlinePolicySize),
			Severity: severity,
		},
		{Label: "Permissions boundary", Value: boundary},
	}
}
