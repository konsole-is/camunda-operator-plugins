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

package rustfs

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// maxNameLength is the longest name that S3 accepts for a bucket.
const maxNameLength = 63

// hashLength is the number of hex characters of the hash in a name. A
// request in another namespace must not find a second name with the same
// hash, or it shares the bucket and the user.
const hashLength = 16

// resourceName returns the one name of the bucket, the user, and the policy
// of the request namespace/request: a valid bucket name of at most 63
// characters. The hash keeps two requests apart whose joined names read the
// same, such as a-b/c and a/b-c, or differ only after the cut.
func resourceName(namespace, request string) string {
	sum := sha256.Sum256([]byte(namespace + "/" + request))
	suffix := "-" + hex.EncodeToString(sum[:])[:hashLength]

	prefix := strings.ReplaceAll(namespace+"-"+request, ".", "-")
	prefix = strings.TrimRight(prefix[:min(len(prefix), maxNameLength-len(suffix))], "-")

	return prefix + suffix
}

// secretName returns the name of the Secret that holds the access key of the
// request.
func secretName(request string) string {
	return request + "-rustfs"
}
