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
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// IAM is the part of the IAM API that the plugins call. Each method behaves
// as the method of the same name of *iam.Client.
type IAM interface {
	GetRole(ctx context.Context, in *iam.GetRoleInput, opts ...func(*iam.Options)) (*iam.GetRoleOutput, error)
	CreateRole(ctx context.Context, in *iam.CreateRoleInput, opts ...func(*iam.Options)) (*iam.CreateRoleOutput, error)
	UpdateAssumeRolePolicy(
		ctx context.Context,
		in *iam.UpdateAssumeRolePolicyInput,
		opts ...func(*iam.Options),
	) (*iam.UpdateAssumeRolePolicyOutput, error)
	PutRolePermissionsBoundary(
		ctx context.Context,
		in *iam.PutRolePermissionsBoundaryInput,
		opts ...func(*iam.Options),
	) (*iam.PutRolePermissionsBoundaryOutput, error)
	TagRole(ctx context.Context, in *iam.TagRoleInput, opts ...func(*iam.Options)) (*iam.TagRoleOutput, error)
	DeleteRole(ctx context.Context, in *iam.DeleteRoleInput, opts ...func(*iam.Options)) (*iam.DeleteRoleOutput, error)
	PutRolePolicy(
		ctx context.Context,
		in *iam.PutRolePolicyInput,
		opts ...func(*iam.Options),
	) (*iam.PutRolePolicyOutput, error)
	GetRolePolicy(
		ctx context.Context,
		in *iam.GetRolePolicyInput,
		opts ...func(*iam.Options),
	) (*iam.GetRolePolicyOutput, error)
	ListRolePolicies(
		ctx context.Context,
		in *iam.ListRolePoliciesInput,
		opts ...func(*iam.Options),
	) (*iam.ListRolePoliciesOutput, error)
	DeleteRolePolicy(
		ctx context.Context,
		in *iam.DeleteRolePolicyInput,
		opts ...func(*iam.Options),
	) (*iam.DeleteRolePolicyOutput, error)
	ListAttachedRolePolicies(
		ctx context.Context,
		in *iam.ListAttachedRolePoliciesInput,
		opts ...func(*iam.Options),
	) (*iam.ListAttachedRolePoliciesOutput, error)
	AttachRolePolicy(
		ctx context.Context,
		in *iam.AttachRolePolicyInput,
		opts ...func(*iam.Options),
	) (*iam.AttachRolePolicyOutput, error)
	DetachRolePolicy(
		ctx context.Context,
		in *iam.DetachRolePolicyInput,
		opts ...func(*iam.Options),
	) (*iam.DetachRolePolicyOutput, error)
	GetPolicy(ctx context.Context, in *iam.GetPolicyInput, opts ...func(*iam.Options)) (*iam.GetPolicyOutput, error)
}

// EKS is the part of the EKS API that the plugins call. Each method behaves
// as the method of the same name of *eks.Client.
type EKS interface {
	CreatePodIdentityAssociation(
		ctx context.Context,
		in *eks.CreatePodIdentityAssociationInput,
		opts ...func(*eks.Options),
	) (*eks.CreatePodIdentityAssociationOutput, error)
	ListPodIdentityAssociations(
		ctx context.Context,
		in *eks.ListPodIdentityAssociationsInput,
		opts ...func(*eks.Options),
	) (*eks.ListPodIdentityAssociationsOutput, error)
	DescribePodIdentityAssociation(
		ctx context.Context,
		in *eks.DescribePodIdentityAssociationInput,
		opts ...func(*eks.Options),
	) (*eks.DescribePodIdentityAssociationOutput, error)
	UpdatePodIdentityAssociation(
		ctx context.Context,
		in *eks.UpdatePodIdentityAssociationInput,
		opts ...func(*eks.Options),
	) (*eks.UpdatePodIdentityAssociationOutput, error)
	DeletePodIdentityAssociation(
		ctx context.Context,
		in *eks.DeletePodIdentityAssociationInput,
		opts ...func(*eks.Options),
	) (*eks.DeletePodIdentityAssociationOutput, error)
}

// STS is the part of the STS API that the plugins call. Each method behaves
// as the method of the same name of *sts.Client.
type STS interface {
	GetCallerIdentity(
		ctx context.Context,
		in *sts.GetCallerIdentityInput,
		opts ...func(*sts.Options),
	) (*sts.GetCallerIdentityOutput, error)
}

var (
	_ IAM = (*iam.Client)(nil)
	_ EKS = (*eks.Client)(nil)
	_ STS = (*sts.Client)(nil)
)

// Clients are the AWS clients of a plugin.
type Clients struct {
	IAM IAM
	EKS EKS
	STS STS
}

// LoadConfig returns the AWS config of the default chain: the environment,
// the shared files, and the web identity or Pod Identity of the pod. An empty
// region takes the region of that chain. The SDK reads AWS_ENDPOINT_URL
// itself, which points every client at one endpoint.
func LoadConfig(ctx context.Context, region string) (aws.Config, error) {
	var opts []func(*config.LoadOptions) error
	if region != "" {
		opts = append(opts, config.WithRegion(region))
	}

	cfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return aws.Config{}, fmt.Errorf("loading the AWS config: %w", err)
	}

	return cfg, nil
}

// NewClients returns the clients of cfg.
func NewClients(cfg aws.Config) Clients {
	return Clients{
		IAM: iam.NewFromConfig(cfg),
		EKS: eks.NewFromConfig(cfg),
		STS: sts.NewFromConfig(cfg),
	}
}
