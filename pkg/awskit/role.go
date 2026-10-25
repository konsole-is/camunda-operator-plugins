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
	"maps"
	"slices"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/iam/types"
)

// RoleSpec is the desired state of an IAM role.
type RoleSpec struct {
	Name        string
	Description string
	// TrustPolicy is the trust policy document, as JSON.
	TrustPolicy []byte
	// PermissionsBoundary is the ARN of the managed policy that bounds the
	// role. Empty sets none on create and leaves the boundary of a role that
	// exists alone.
	PermissionsBoundary string
	Tags                map[string]string
}

// Role is an IAM role.
type Role struct {
	Name, ARN string
}

// RoleChange says what EnsureRole changed: Created, or the parts that it
// updated, from "trust policy", "permissions boundary", and "tags", in that
// order. The zero value means that the role was as spec says.
type RoleChange struct {
	Created bool
	Updated []string
}

// EnsureRole creates the role of spec, or updates a role of that name to the
// trust policy, the permissions boundary, and the tags of spec. It never
// removes a tag that spec does not name. A role that another writer creates
// at the same time is updated as one that existed.
func EnsureRole(ctx context.Context, c IAM, spec RoleSpec) (Role, RoleChange, error) {
	out, err := c.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(spec.Name)})
	if IsNotFound(err) {
		created, createErr := c.CreateRole(ctx, &iam.CreateRoleInput{
			RoleName:                 aws.String(spec.Name),
			Description:              aws.String(spec.Description),
			AssumeRolePolicyDocument: aws.String(string(spec.TrustPolicy)),
			PermissionsBoundary:      optional(spec.PermissionsBoundary),
			Tags:                     iamTags(spec.Tags),
		})
		if createErr == nil {
			return Role{Name: spec.Name, ARN: aws.ToString(created.Role.Arn)}, RoleChange{Created: true}, nil
		}
		if !IsAlreadyExists(createErr) {
			return Role{}, RoleChange{}, fmt.Errorf("creating the IAM role %s: %w", spec.Name, createErr)
		}

		out, err = c.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(spec.Name)})
	}
	if err != nil {
		return Role{}, RoleChange{}, fmt.Errorf("reading the IAM role %s: %w", spec.Name, err)
	}

	role := Role{Name: spec.Name, ARN: aws.ToString(out.Role.Arn)}
	updated, err := updateRole(ctx, c, out.Role, spec)
	return role, RoleChange{Updated: updated}, err
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return aws.String(s)
}

func iamTags(tags map[string]string) []types.Tag {
	out := make([]types.Tag, 0, len(tags))
	for _, k := range slices.Sorted(maps.Keys(tags)) {
		out = append(out, types.Tag{Key: aws.String(k), Value: aws.String(tags[k])})
	}
	return out
}

// updateRole brings the live role to spec and returns the parts that it
// updated.
func updateRole(ctx context.Context, c IAM, live *types.Role, spec RoleSpec) ([]string, error) {
	var updated []string

	trust, err := DecodePolicy(aws.ToString(live.AssumeRolePolicyDocument))
	if err != nil || !SamePolicy(trust, spec.TrustPolicy) {
		_, err := c.UpdateAssumeRolePolicy(ctx, &iam.UpdateAssumeRolePolicyInput{
			RoleName:       aws.String(spec.Name),
			PolicyDocument: aws.String(string(spec.TrustPolicy)),
		})
		if err != nil {
			return updated, fmt.Errorf("updating the trust policy of the IAM role %s: %w", spec.Name, err)
		}
		updated = append(updated, "trust policy")
	}

	liveBoundary := ""
	if live.PermissionsBoundary != nil {
		liveBoundary = aws.ToString(live.PermissionsBoundary.PermissionsBoundaryArn)
	}
	if spec.PermissionsBoundary != "" && liveBoundary != spec.PermissionsBoundary {
		_, err := c.PutRolePermissionsBoundary(ctx, &iam.PutRolePermissionsBoundaryInput{
			RoleName:            aws.String(spec.Name),
			PermissionsBoundary: aws.String(spec.PermissionsBoundary),
		})
		if err != nil {
			return updated, fmt.Errorf("setting the permissions boundary of the IAM role %s: %w", spec.Name, err)
		}
		updated = append(updated, "permissions boundary")
	}

	liveTags := make(map[string]string, len(live.Tags))
	for _, t := range live.Tags {
		liveTags[aws.ToString(t.Key)] = aws.ToString(t.Value)
	}
	for k, v := range spec.Tags {
		if got, ok := liveTags[k]; !ok || got != v {
			if _, err := c.TagRole(ctx, &iam.TagRoleInput{
				RoleName: aws.String(spec.Name),
				Tags:     iamTags(spec.Tags),
			}); err != nil {
				return updated, fmt.Errorf("tagging the IAM role %s: %w", spec.Name, err)
			}
			updated = append(updated, "tags")
			break
		}
	}

	return updated, nil
}

// DeleteRole deletes the inline policies of the role name, detaches its
// managed policies, and deletes it. It reports false, with no error, when the
// role does not exist.
func DeleteRole(ctx context.Context, c IAM, name string) (bool, error) {
	policies, err := policyNames(ctx, c, name)
	if IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	for _, p := range policies {
		if err := DeleteInlinePolicy(ctx, c, name, p); err != nil {
			return false, err
		}
	}

	attached, err := attachedPolicies(ctx, c, name)
	if err != nil {
		return false, err
	}

	for _, arn := range attached {
		_, err := c.DetachRolePolicy(
			ctx,
			&iam.DetachRolePolicyInput{RoleName: aws.String(name), PolicyArn: aws.String(arn)},
		)
		if err != nil && !IsNotFound(err) {
			return false, fmt.Errorf("detaching the policy %s from the IAM role %s: %w", arn, name, err)
		}
	}

	_, err = c.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String(name)})
	if IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("deleting the IAM role %s: %w", name, err)
	}

	return true, nil
}

// policyNames returns the name of each inline policy of the role, from every
// page. The error of a missing role keeps its code.
func policyNames(ctx context.Context, c IAM, role string) ([]string, error) {
	var names []string
	var marker *string
	for {
		out, err := c.ListRolePolicies(ctx, &iam.ListRolePoliciesInput{RoleName: aws.String(role), Marker: marker})
		if err != nil {
			return nil, fmt.Errorf("listing the inline policies of the IAM role %s: %w", role, err)
		}

		names = append(names, out.PolicyNames...)
		if !out.IsTruncated {
			return names, nil
		}
		marker = out.Marker
	}
}

func attachedPolicies(ctx context.Context, c IAM, role string) ([]string, error) {
	var arns []string
	var marker *string
	for {
		out, err := c.ListAttachedRolePolicies(
			ctx,
			&iam.ListAttachedRolePoliciesInput{RoleName: aws.String(role), Marker: marker},
		)
		if err != nil {
			return nil, fmt.Errorf("listing the managed policies of the IAM role %s: %w", role, err)
		}

		for _, p := range out.AttachedPolicies {
			arns = append(arns, aws.ToString(p.PolicyArn))
		}
		if !out.IsTruncated {
			return arns, nil
		}
		marker = out.Marker
	}
}

// InlinePolicies returns each inline policy of the role, decoded, by name.
func InlinePolicies(ctx context.Context, c IAM, role string) (map[string][]byte, error) {
	names, err := policyNames(ctx, c, role)
	if err != nil {
		return nil, err
	}

	docs := make(map[string][]byte, len(names))
	for _, name := range names {
		out, err := c.GetRolePolicy(
			ctx,
			&iam.GetRolePolicyInput{RoleName: aws.String(role), PolicyName: aws.String(name)},
		)
		if err != nil {
			return nil, fmt.Errorf("reading the inline policy %s of the IAM role %s: %w", name, role, err)
		}

		if docs[name], err = DecodePolicy(aws.ToString(out.PolicyDocument)); err != nil {
			return nil, fmt.Errorf("the inline policy %s of the IAM role %s: %w", name, role, err)
		}
	}

	return docs, nil
}

// PutInlinePolicy adds or replaces the inline policy name of the role. The
// error of a refused document keeps its AWS code, for IsMalformedPolicy and
// IsLimitExceeded.
func PutInlinePolicy(ctx context.Context, c IAM, role, name string, doc []byte) error {
	_, err := c.PutRolePolicy(ctx, &iam.PutRolePolicyInput{
		RoleName:       aws.String(role),
		PolicyName:     aws.String(name),
		PolicyDocument: aws.String(string(doc)),
	})
	if err != nil {
		return fmt.Errorf("putting the inline policy %s on the IAM role %s: %w", name, role, err)
	}
	return nil
}

// DeleteInlinePolicy deletes the inline policy name of the role. A policy
// that is gone is not an error.
func DeleteInlinePolicy(ctx context.Context, c IAM, role, name string) error {
	_, err := c.DeleteRolePolicy(
		ctx,
		&iam.DeleteRolePolicyInput{RoleName: aws.String(role), PolicyName: aws.String(name)},
	)
	if err != nil && !IsNotFound(err) {
		return fmt.Errorf("deleting the inline policy %s of the IAM role %s: %w", name, role, err)
	}
	return nil
}
