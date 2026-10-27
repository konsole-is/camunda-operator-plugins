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
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws/arn"
)

// OIDCProvider is an IAM OIDC identity provider, such as the one of an EKS
// cluster.
type OIDCProvider struct {
	// ARN is the ARN of the provider.
	ARN string
	// Issuer is the issuer URL of the provider without its scheme, for
	// example oidc.eks.eu-west-1.amazonaws.com/id/ABC. Trust policy conditions
	// start with it.
	Issuer string
}

// ParseOIDCProvider returns the provider of providerARN. It refuses an ARN
// that is not an IAM oidc-provider ARN with an issuer.
func ParseOIDCProvider(providerARN string) (OIDCProvider, error) {
	parsed, err := arn.Parse(providerARN)
	issuer, ok := strings.CutPrefix(parsed.Resource, "oidc-provider/")
	if err != nil || parsed.Service != "iam" || !ok || issuer == "" {
		return OIDCProvider{}, fmt.Errorf("%q is not the ARN of an IAM OIDC provider", providerARN)
	}

	return OIDCProvider{ARN: providerARN, Issuer: issuer}, nil
}

// consoles are the hosts of the AWS console by partition.
var consoles = map[string]string{
	"aws":        "console.aws.amazon.com",
	"aws-cn":     "console.amazonaws.cn",
	"aws-us-gov": "console.amazonaws-us-gov.com",
}

// RoleConsoleURL returns the page of the role roleARN in the IAM console of
// its partition. It refuses an ARN that is not an IAM role ARN, and a
// partition without a known console.
func RoleConsoleURL(roleARN string) (string, error) {
	parsed, err := arn.Parse(roleARN)
	name, ok := strings.CutPrefix(parsed.Resource, "role/")
	if err != nil || parsed.Service != "iam" || !ok || name == "" {
		return "", fmt.Errorf("%q is not the ARN of an IAM role", roleARN)
	}

	host, ok := consoles[parsed.Partition]
	if !ok {
		return "", fmt.Errorf(
			"the role %s is in the partition %q, which has no known console",
			roleARN,
			parsed.Partition,
		)
	}

	return "https://" + host + "/iam/home#/roles/details/" + name[strings.LastIndex(name, "/")+1:], nil
}
