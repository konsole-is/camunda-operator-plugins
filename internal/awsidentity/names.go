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
	"crypto/sha256"
	"encoding/hex"
	"slices"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
)

// The names that the hub gives and publishes.
const (
	// name is the controller name of the plugin.
	name = "aws-identity"
	// rolePrefix starts the name of each role of the hub.
	rolePrefix = "camunda"
	// policyPrefix starts the name of each inline policy that the hub writes
	// for a Grant. The hub removes only policies with this prefix.
	policyPrefix = "grant-"
	// maxPolicyName is the longest name of an inline policy in IAM.
	maxPolicyName = 128

	outputPrincipal   = "principal"
	outputAnnotations = "serviceAccountAnnotations"
	annotationRoleARN = "eks.amazonaws.com/role-arn"
)

// policyName returns the name of the inline policy of the Grant grant. It
// fits the 128 characters of IAM: a longer name is cut and ends with _ and a
// hash. A Kubernetes name has no _, so no Grant name gives the cut name of
// another.
func policyName(grant string) string {
	name := policyPrefix + grant
	if len(name) <= maxPolicyName {
		return name
	}

	sum := sha256.Sum256([]byte(grant))
	return name[:maxPolicyName-9] + "_" + hex.EncodeToString(sum[:])[:8]
}

// serviceAccounts returns the ServiceAccounts that the role of target
// trusts: target.serviceAccountName and each additional one, without empty
// names, sorted, each once.
func serviceAccounts(target pluginsv1alpha1.TargetRef) []string {
	names := slices.DeleteFunc(
		append([]string{target.ServiceAccountName}, target.AdditionalServiceAccounts...),
		func(s string) bool { return s == "" },
	)
	slices.Sort(names)
	return slices.Compact(names)
}
