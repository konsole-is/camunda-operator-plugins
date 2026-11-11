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

package awsebs

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
)

// maxNameLength is the length of a DNS label, so that the name of a
// StorageClass is also a valid label value.
const maxNameLength = 63

// StorageClassName returns the name of the StorageClass of a request:
// camunda-<namespace>-<claim>, each dot of claim as a dash, and a dash and 16
// hex digits of a hash of namespace/request. The hash is always there, because
// two requests whose joined names read the same, such as a-b/c and a/b-c, and
// two long names that differ only after the cut must get two names. The name
// has at most 63 characters.
func StorageClassName(namespace, claim, request string) string {
	sum := sha256.Sum256([]byte(namespace + "/" + request))
	suffix := "-" + hex.EncodeToString(sum[:])[:16]

	readable := "camunda-" + namespace + "-" + strings.ReplaceAll(claim, ".", "-")
	return awskit.FitName(readable, maxNameLength-len(suffix)) + suffix
}
