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
	"maps"
	"regexp"
	"slices"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
)

// maxAlias is the KMS limit on the length of an alias name.
const maxAlias = 256

var aliasUnsafe = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// aliasName returns alias/<prefix>/<namespace>/<claim>, at most 256
// characters. Each character of namespace and claim that KMS does not allow,
// and each /, becomes _, so that the alias keeps its three levels.
func aliasName(prefix, namespace, claim string) string {
	name := "alias/" + prefix + "/" +
		aliasUnsafe.ReplaceAllString(namespace, "_") + "/" +
		aliasUnsafe.ReplaceAllString(claim, "_")
	return awskit.FitName(name, maxAlias)
}

// keyTags returns the request tags of ref, its UID tag, and the extra tags
// of s, sorted by key.
func keyTags(s Settings, ref keyRef) []types.Tag {
	tags := s.ExtraTags.RequestTags(ref.Namespace, ref.Claim, ref.Request)
	tags[tagRequestUID] = ref.UID

	out := make([]types.Tag, 0, len(tags))
	for _, key := range slices.Sorted(maps.Keys(tags)) {
		out = append(out, types.Tag{TagKey: aws.String(key), TagValue: aws.String(tags[key])})
	}
	return out
}
