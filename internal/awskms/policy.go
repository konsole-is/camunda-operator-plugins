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
	"encoding/json"
	"fmt"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
)

// statement is one statement of a key policy. Its fields marshal in this
// order.
type statement struct {
	Sid       string
	Effect    string
	Principal map[string]any
	Action    []string
	Resource  string
	Condition map[string]map[string]string `json:",omitempty"`
}

// keyPolicy returns the key policy of each key, and custom true. When s has
// no key administrators and no CSI driver role, it returns no document and
// custom false: the keys keep the default key policy.
func keyPolicy(s Settings) (doc []byte, custom bool, err error) {
	if len(s.KeyAdministratorARNs) == 0 && s.CSIDriverRoleARN == "" {
		return nil, false, nil
	}

	// The statement of the default key policy, so that IAM policies can grant
	// the use of the key.
	statements := []statement{{
		Sid:       "AccountRoot",
		Principal: map[string]any{"AWS": "arn:" + s.Partition + ":iam::" + s.Account + ":root"},
		Action:    []string{"kms:*"},
	}}

	if len(s.KeyAdministratorARNs) > 0 {
		statements = append(statements, statement{
			Sid:       "KeyAdministrators",
			Principal: map[string]any{"AWS": s.KeyAdministratorARNs},
			Action: []string{
				"kms:Create*", "kms:Describe*", "kms:Enable*", "kms:List*", "kms:Put*", "kms:Update*",
				"kms:Revoke*", "kms:Disable*", "kms:Get*", "kms:Delete*", "kms:TagResource",
				"kms:UntagResource", "kms:ScheduleKeyDeletion", "kms:CancelKeyDeletion", "kms:ReplicateKey",
			},
		})
	}

	if s.CSIDriverRoleARN != "" {
		driver := map[string]any{"AWS": s.CSIDriverRoleARN}
		statements = append(statements,
			statement{
				Sid:       "EBSCSIDriverUse",
				Principal: driver,
				Action: []string{
					"kms:Encrypt", "kms:Decrypt", "kms:ReEncrypt*", "kms:GenerateDataKey*", "kms:DescribeKey",
				},
			},
			// The driver creates grants only for the EBS volumes it attaches.
			statement{
				Sid:       "EBSCSIDriverGrants",
				Principal: driver,
				Action:    []string{"kms:CreateGrant"},
				Condition: map[string]map[string]string{"Bool": {"kms:GrantIsForAWSResource": "true"}},
			},
		)
	}

	raw := make([]json.RawMessage, 0, len(statements))
	for _, st := range statements {
		st.Effect, st.Resource = "Allow", "*"
		b, err := json.Marshal(st)
		if err != nil {
			return nil, false, fmt.Errorf("encoding the key policy statement %s: %w", st.Sid, err)
		}
		raw = append(raw, b)
	}

	doc, err = awskit.PolicyDocument(raw...)
	if err != nil {
		return nil, false, err
	}
	return doc, true, nil
}
