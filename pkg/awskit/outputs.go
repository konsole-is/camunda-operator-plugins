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

package awskit

import (
	"encoding/json"
)

// The outputs of a request whose pods assume a role, and the annotation of
// IRSA.
const (
	// OutputPrincipal is the ARN of the role.
	OutputPrincipal = "principal"
	// OutputServiceAccountAnnotations is a JSON object of the annotations
	// that the ServiceAccounts of the pods need.
	OutputServiceAccountAnnotations = "serviceAccountAnnotations"
	// AnnotationRoleARN is the ServiceAccount annotation that gives a pod
	// its role with IRSA.
	AnnotationRoleARN = "eks.amazonaws.com/role-arn"
)

// IdentityOutputs returns the outputs OutputPrincipal and
// OutputServiceAccountAnnotations of the role roleARN that pods assume in
// mode. With ModePodIdentity the annotations are {}, because an association
// gives the role.
func IdentityOutputs(mode Mode, roleARN string) map[string]string {
	annotations := map[string]string{}
	if mode == ModeIRSA {
		annotations[AnnotationRoleARN] = roleARN
	}

	// A map of strings always encodes.
	raw, _ := json.Marshal(annotations)
	return map[string]string{OutputPrincipal: roleARN, OutputServiceAccountAnnotations: string(raw)}
}
