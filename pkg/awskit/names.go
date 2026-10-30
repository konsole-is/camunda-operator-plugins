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
	"fmt"
	"regexp"
	"strings"
)

// MaxRolePrefix is the longest prefix of RoleName that gives a name inside
// the 64 characters of IAM.
const MaxRolePrefix = 47

// MaxAliasPrefix is the longest alias prefix. With a namespace of 63 and a
// claim of 40 characters, alias/<prefix>/<namespace>/<claim> stays inside the
// 256 characters of KMS.
const MaxAliasPrefix = 128

var (
	rolePrefixChars  = regexp.MustCompile(`^[A-Za-z0-9+=,.@_-]+$`)
	aliasPrefixChars = regexp.MustCompile(`^[A-Za-z0-9/_-]+$`)
)

// RoleName returns the name of the role of a claim: prefix, a dash, and 16
// hex digits. scope names the Kubernetes cluster, such as the OIDC issuer of
// an EKS cluster, because two clusters in one AWS account can have the same
// namespace and claim. Two sets of scope, namespace, and claim get two names.
// The name fits the 64 characters of IAM when prefix has at most
// MaxRolePrefix.
func RoleName(prefix, scope, namespace, claim string) string {
	sum := sha256.Sum256([]byte(scope + "\n" + namespace + "/" + claim))
	return prefix + "-" + hex.EncodeToString(sum[:])[:16]
}

// CheckRolePrefix returns an error when RoleName with this prefix can give a
// name that IAM refuses. The prefix must have 1 to MaxRolePrefix characters
// of A-Z, a-z, 0-9, and +=,.@_-.
func CheckRolePrefix(prefix string) error {
	if len(prefix) > MaxRolePrefix {
		return fmt.Errorf("role name prefix %q: at most %d characters", prefix, MaxRolePrefix)
	}

	if !rolePrefixChars.MatchString(prefix) {
		return fmt.Errorf("role name prefix %q: one or more of A-Z, a-z, 0-9, and +=,.@_-", prefix)
	}

	return nil
}

// CheckAliasPrefix returns an error when prefix is not a valid KMS alias
// prefix. The prefix must have 1 to MaxAliasPrefix characters of A-Z, a-z,
// 0-9, and /_-, and must not start or end with "/". It must not be "aws" or
// start with "aws/", which AWS reserves, or start with "alias/", which the
// plugin adds.
func CheckAliasPrefix(prefix string) error {
	switch {
	case len(prefix) > MaxAliasPrefix:
		return fmt.Errorf("alias prefix %q: at most %d characters", prefix, MaxAliasPrefix)
	case !aliasPrefixChars.MatchString(prefix):
		return fmt.Errorf("alias prefix %q: one or more of A-Z, a-z, 0-9, and /_-", prefix)
	case strings.HasPrefix(prefix, "/") || strings.HasSuffix(prefix, "/"):
		return fmt.Errorf("alias prefix %q: must not start or end with /", prefix)
	case prefix == "aws" || strings.HasPrefix(prefix, "aws/"):
		return fmt.Errorf("alias prefix %q: AWS reserves alias/aws/", prefix)
	case strings.HasPrefix(prefix, "alias/"):
		return fmt.Errorf("alias prefix %q: the plugin adds alias/ itself", prefix)
	}

	return nil
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
