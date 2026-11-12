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

import "github.com/konsole-is/camunda-operator-plugins/pkg/awskit"

// defaultBucketPrefix starts the name of a bucket whose request sets no
// bucketPrefix.
const defaultBucketPrefix = "camunda"

// bucketName returns <prefix>-<namespace>-<claim>-<16 hex>. A prefix that the
// parameter schema allows always stays whole.
func bucketName(prefix, namespace, claim string) string {
	return awskit.BucketName(prefix, namespace, claim)
}

// bucketTags returns the extra tags of s, the request tags of ref, and its
// UID tag.
func bucketTags(s Settings, ref bucketRef) map[string]string {
	return s.ExtraTags.OwnerTags(ref.Namespace, ref.Claim, ref.Request, ref.UID)
}
