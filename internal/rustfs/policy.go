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

import "encoding/json"

// policyDocument is an IAM policy, as RustFS reads it.
type policyDocument struct {
	Version   string      `json:"Version"`
	Statement []statement `json:"Statement"`
}

type statement struct {
	Effect   string   `json:"Effect"`
	Action   []string `json:"Action"`
	Resource []string `json:"Resource"`
}

// userPolicy returns the policy of the user of bucket. It allows the reads and
// the writes of a backup on bucket and its objects, and nothing else.
func userPolicy(bucket string) []byte {
	arn := "arn:aws:s3:::" + bucket
	doc := policyDocument{
		Version: "2012-10-17",
		Statement: []statement{
			{
				Effect: "Allow",
				Action: []string{
					"s3:GetBucketLocation",
					"s3:ListBucket",
					"s3:ListBucketMultipartUploads",
					"s3:ListBucketVersions",
				},
				Resource: []string{arn},
			},
			{
				Effect: "Allow",
				Action: []string{
					"s3:AbortMultipartUpload",
					"s3:DeleteObject",
					"s3:GetObject",
					"s3:ListMultipartUploadParts",
					"s3:PutObject",
				},
				Resource: []string{arn + "/*"},
			},
		},
	}

	// A struct of strings always encodes.
	raw, _ := json.Marshal(doc)
	return raw
}
