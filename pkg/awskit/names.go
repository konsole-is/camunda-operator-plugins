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
	"crypto/sha256"
	"encoding/hex"
)

// The tags that tie an AWS object to the request that it serves.
const (
	TagNamespace = "camunda.io/namespace"
	TagClaim     = "camunda.io/claim"
	TagRequest   = "camunda.io/request"
)

// RoleName returns the name of the role of a claim: prefix, a dash, and 16
// hex digits of a hash of the namespace and the claim. Two pairs of namespace
// and claim get two names. The name fits the 64 characters of IAM when prefix
// has at most 47.
func RoleName(prefix, namespace, claim string) string {
	sum := sha256.Sum256([]byte(namespace + "/" + claim))
	return prefix + "-" + hex.EncodeToString(sum[:])[:16]
}

// FitName returns name when it has at most maxLen bytes. Else it returns a
// name of exactly maxLen bytes: the start of name, a dash, and 8 hex digits
// of a hash of name, so that two long names stay apart. maxLen must be more
// than 9.
func FitName(name string, maxLen int) string {
	if len(name) <= maxLen {
		return name
	}

	sum := sha256.Sum256([]byte(name))
	return name[:maxLen-9] + "-" + hex.EncodeToString(sum[:])[:8]
}

// RequestTags returns the tags of an AWS object of a request.
func RequestTags(namespace, claim, request string) map[string]string {
	return map[string]string{
		TagNamespace: namespace,
		TagClaim:     claim,
		TagRequest:   request,
	}
}
