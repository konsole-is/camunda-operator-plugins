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

package awss3

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// defaultBucketPrefix starts the name of a bucket whose request sets no
// bucketPrefix.
const defaultBucketPrefix = "camunda"

const (
	// maxBucketName is the longest name that S3 accepts for a bucket.
	maxBucketName = 63
	// hashLength is the number of hex digits of the hash in a name.
	hashLength = 16
)

// bucketName returns <prefix>-<namespace>-<claim>-<16 hex>, at most 63
// characters, with each "." of namespace and claim as "-". The hash is of
// prefix/namespace/claim, so a-b/c and a/b-c get two names, and so do two
// prefixes or claims whose names differ only after the cut. A prefix that the
// parameter schema allows always stays whole.
func bucketName(prefix, namespace, claim string) string {
	sum := sha256.Sum256([]byte(prefix + "/" + namespace + "/" + claim))
	suffix := "-" + hex.EncodeToString(sum[:])[:hashLength]

	readable := strings.ReplaceAll(prefix+"-"+namespace+"-"+claim, ".", "-")
	readable = strings.TrimRight(readable[:min(len(readable), maxBucketName-len(suffix))], "-")

	return readable + suffix
}

// bucketTags returns the extra tags of s, the request tags of ref, and its
// UID tag.
func bucketTags(s Settings, ref bucketRef) map[string]string {
	return s.ExtraTags.OwnerTags(ref.Namespace, ref.Claim, ref.Request, ref.UID)
}
