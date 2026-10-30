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
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
)

var errInlinePolicyTooLarge = errors.New("the rendered inline policy is over the IAM size limit")

// trustedServiceAccounts returns the ServiceAccount that the role of target
// trusts: target.serviceAccountName only, or none when it is empty. The
// additional ServiceAccounts are those of the cluster, not of the Connectors.
func trustedServiceAccounts(target pluginsv1alpha1.TargetRef) []string {
	if target.ServiceAccountName == "" {
		return nil
	}
	return []string{target.ServiceAccountName}
}

// wantedPolicies returns the managed and the extra policies of p, sorted,
// each once.
func wantedPolicies(p Params) []string {
	return slices.Compact(slices.Sorted(slices.Values(append(slices.Clone(p.ManagedPolicyARNs), p.ExtraPolicyARNs...))))
}

// inlinePolicyDocument returns raw compact, and its size as IAM counts it.
// Absent or null raw is no policy: nil and 0. A document over
// awskit.MaxInlinePolicySize is errInlinePolicyTooLarge.
func inlinePolicyDocument(raw json.RawMessage) ([]byte, int, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, 0, nil
	}

	var doc bytes.Buffer
	if err := json.Compact(&doc, raw); err != nil {
		return nil, 0, fmt.Errorf("compacting the inline policy: %w", err)
	}

	size := awskit.PolicySize(doc.Bytes())
	if size > awskit.MaxInlinePolicySize {
		return nil, size, fmt.Errorf(
			"%w: %d characters without white space, the limit is %d",
			errInlinePolicyTooLarge,
			size,
			awskit.MaxInlinePolicySize,
		)
	}

	return doc.Bytes(), size, nil
}

// summary returns the summary of a role with attached managed policies and
// an inline policy of size characters, with a Warning above 80% of the
// limit.
func summary(role awskit.Role, attached, size int, boundary string) []pluginsv1alpha1.SummaryItem {
	severity := pluginsv1alpha1.SeverityInfo
	if size*100 > 80*awskit.MaxInlinePolicySize {
		severity = pluginsv1alpha1.SeverityWarning
	}

	return []pluginsv1alpha1.SummaryItem{
		{Label: "Role", Value: role.Name},
		{Label: "Attached policies", Value: strconv.Itoa(attached)},
		{
			Label:    "Inline policy",
			Value:    fmt.Sprintf("%d of %d characters", size, awskit.MaxInlinePolicySize),
			Severity: severity,
		},
		{Label: "Permissions boundary", Value: boundary},
	}
}
