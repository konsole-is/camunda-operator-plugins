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
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/arn"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
)

// Settings are the settings of the plugin that the chart gives.
type Settings struct {
	// Region is the home region: the region of each primary key.
	Region string
	// Partition and Account are the partition and the account of the
	// credentials, for the account root of the key policy.
	Partition, Account string
	// AliasPrefix starts each alias, after alias/.
	AliasPrefix string
	// ExtraTags go on each key and each replica, next to the request tags.
	ExtraTags awskit.ExtraTags
	// KeyAdministratorARNs are the IAM principals that administer each key.
	KeyAdministratorARNs []string
	// CSIDriverRoleARN is the IAM role of the EBS CSI driver, which uses each
	// key. When it and KeyAdministratorARNs are both empty, each key keeps the
	// default key policy.
	CSIDriverRoleARN string
}

// Validate returns an error that names the first field that is not valid: an
// empty region, partition, or account, an alias prefix that
// awskit.CheckAliasPrefix refuses, or a principal that is not an IAM ARN.
func (s Settings) Validate() error {
	switch {
	case s.Region == "":
		return errors.New("region: empty")
	case s.Partition == "" || s.Account == "":
		return errors.New("account: the partition or the account is empty")
	}

	if err := awskit.CheckAliasPrefix(s.AliasPrefix); err != nil {
		return fmt.Errorf("alias prefix: %w", err)
	}

	for _, principal := range s.KeyAdministratorARNs {
		if !isIAMARN(principal) {
			return fmt.Errorf("key administrator ARNs: %q is not the ARN of an IAM principal", principal)
		}
	}

	if s.CSIDriverRoleARN != "" && !isIAMARN(s.CSIDriverRoleARN) {
		return fmt.Errorf("CSI driver role ARN: %q is not the ARN of an IAM principal", s.CSIDriverRoleARN)
	}

	return nil
}

func isIAMARN(s string) bool {
	parsed, err := arn.Parse(s)
	return err == nil && parsed.Service == "iam"
}

// ResolveAccount returns the account and the partition of the credentials of
// c.
func ResolveAccount(ctx context.Context, c awskit.STS) (account, partition string, err error) {
	out, err := c.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return "", "", fmt.Errorf("reading the AWS caller identity: %w", err)
	}

	parsed, err := arn.Parse(aws.ToString(out.Arn))
	if err != nil {
		return "", "", fmt.Errorf("reading the partition of the caller %q: %w", aws.ToString(out.Arn), err)
	}

	return aws.ToString(out.Account), parsed.Partition, nil
}
