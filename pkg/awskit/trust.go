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
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

// Mode is how pods of an EKS cluster assume a role.
type Mode string

// The modes of the identity hub.
const (
	// ModeIRSA is IAM roles for service accounts: the role trusts the OIDC
	// provider of the cluster for each ServiceAccount.
	ModeIRSA Mode = "irsa"
	// ModePodIdentity is EKS Pod Identity: the role trusts the EKS service,
	// and an association ties each ServiceAccount to the role.
	ModePodIdentity Mode = "podIdentity"
)

// ParseMode returns the mode s names.
func ParseMode(s string) (Mode, error) {
	switch m := Mode(s); m {
	case ModeIRSA, ModePodIdentity:
		return m, nil
	default:
		return "", fmt.Errorf("the mode %q is not %s or %s", s, ModeIRSA, ModePodIdentity)
	}
}

// TrustPolicy returns the trust policy of a role that the serviceAccounts of
// namespace assume in mode. With ModeIRSA it trusts provider for each
// ServiceAccount and no other. With ModePodIdentity it trusts the EKS Pod
// Identity service, and the associations decide which ServiceAccounts get
// the role. With no serviceAccounts it denies the principal of mode, so that
// nobody assumes the role. It refuses ModeIRSA without a provider.
func TrustPolicy(mode Mode, provider OIDCProvider, namespace string, serviceAccounts []string) ([]byte, error) {
	effect := "Allow"
	if len(serviceAccounts) == 0 {
		effect = "Deny"
	}

	var statement map[string]any
	switch mode {
	case ModeIRSA:
		if provider.ARN == "" || provider.Issuer == "" {
			return nil, errors.New("a trust policy for IRSA needs the OIDC provider of the EKS cluster")
		}

		statement = map[string]any{
			"Effect":    effect,
			"Principal": map[string]string{"Federated": provider.ARN},
			"Action":    "sts:AssumeRoleWithWebIdentity",
		}
		if len(serviceAccounts) > 0 {
			subjects := make([]string, 0, len(serviceAccounts))
			for _, sa := range serviceAccounts {
				subjects = append(subjects, "system:serviceaccount:"+namespace+":"+sa)
			}
			slices.Sort(subjects)

			statement["Condition"] = map[string]any{"StringEquals": map[string]any{
				provider.Issuer + ":aud": "sts.amazonaws.com",
				provider.Issuer + ":sub": slices.Compact(subjects),
			}}
		}
	case ModePodIdentity:
		statement = map[string]any{
			"Effect":    effect,
			"Principal": map[string]string{"Service": "pods.eks.amazonaws.com"},
			"Action":    []string{"sts:AssumeRole", "sts:TagSession"},
		}
	default:
		return nil, fmt.Errorf("the mode %q is not %s or %s", mode, ModeIRSA, ModePodIdentity)
	}

	raw, err := json.Marshal(statement)
	if err != nil {
		return nil, fmt.Errorf("encoding the trust statement: %w", err)
	}

	return PolicyDocument(raw)
}
