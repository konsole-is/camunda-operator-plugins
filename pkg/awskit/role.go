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
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

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

// DesiredHash returns 16 hex digits of a hash of the trust policy, the
// permissions boundary, and the tags of s without TagDesired, and then of
// each part of extra. Two lists of parts that differ give two hashes, also
// when their bytes join to the same text.
func (s RoleSpec) DesiredHash(extra ...[]byte) string {
	tags := maps.Clone(s.Tags)
	delete(tags, TagDesired)

	var input []byte
	for _, part := range append([][]byte{
		s.TrustPolicy,
		[]byte(s.PermissionsBoundary),
		[]byte(ExtraTags(tags).String()),
	}, extra...) {
		input = binary.BigEndian.AppendUint64(input, uint64(len(part)))
		input = append(input, part...)
	}
	sum := sha256.Sum256(input)
	return hex.EncodeToString(sum[:8])
}

// Role is an IAM role.
type Role struct {
	Name, ARN string
	// Desired is the TagDesired value of the role as EnsureRole or ClaimRole
	// read it, before any change. It is empty for a role that EnsureRole
	// created.
	Desired string
}

// RoleInUseError means that an IAM role has the name that a request wants,
// and its TagRequestUID tag names another request, or none.
type RoleInUseError struct {
	// Name is the role. Owner names the request of its tags, such as "the
	// request team-a/orders-identity with the UID 1f2e", or "no request".
	Name, Owner string
}

func (e RoleInUseError) Error() string {
	return "The IAM role " + e.Name + " belongs to " + e.Owner + ". Delete the role, or change the role name prefix"
}

// RoleChange says what EnsureRole changed: Created, or the parts that it
// updated, from "trust policy", "permissions boundary", and "tags", in that
// order. The zero value means that the role was as spec says.
type RoleChange struct {
	Created bool
	Updated []string
}

// Corrections names each part of the role name that c updated, such as "the
// trust policy of the IAM role camunda-0123", for pluginkit.Result.Corrected.
func (c RoleChange) Corrections(name string) []string {
	out := make([]string, 0, len(c.Updated))
	for _, part := range c.Updated {
		out = append(out, "the "+part+" of the IAM role "+name)
	}
	return out
}

// EnsureRole creates the role of spec, or updates a role of that name to the
// trust policy, the permissions boundary, and the tags of spec. It never
// removes a tag that spec does not name. A role that another writer creates
// at the same time is updated as one that existed.
//
// It refuses with a RoleInUseError, and changes nothing, a live role whose
// TagRequestUID value differs from the one in spec.Tags. An absent tag is the
// empty value. It takes a live role without a TagRequestUID value whose
// TagNamespace and TagRequest are the ones in spec.Tags, and writes its UID.
//
// hash is the DesiredHash of the pass. When the TagDesired value of the live
// role is another hash, EnsureRole sets it to DesiredChanging, with the tags
// of spec, before it changes anything else. So a change that fails part of
// the way is not drift in a later pass.
func EnsureRole(ctx context.Context, c IAM, spec RoleSpec, hash string) (Role, RoleChange, error) {
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

	live := roleTags(out.Role)
	if !ownedBy(live, spec.Tags) {
		return Role{}, RoleChange{}, RoleInUseError{Name: spec.Name, Owner: ownerOf(live)}
	}

	role := Role{Name: spec.Name, ARN: aws.ToString(out.Role.Arn), Desired: live[TagDesired]}
	if role.Desired != "" && role.Desired != hash && role.Desired != DesiredChanging {
		if _, err := MarkDesired(ctx, c, role, spec.Tags, DesiredChanging); err != nil {
			return role, RoleChange{}, err
		}
	}

	updated, err := updateRole(ctx, c, out.Role, live, spec)
	return role, RoleChange{Updated: updated}, err
}

func roleTags(role *types.Role) map[string]string {
	tags := make(map[string]string, len(role.Tags))
	for _, t := range role.Tags {
		tags[aws.ToString(t.Key)] = aws.ToString(t.Value)
	}
	return tags
}

// ownedBy reports whether a role with the tags live belongs to the request of
// the tags want.
func ownedBy(live, want map[string]string) bool {
	if live[TagRequestUID] == want[TagRequestUID] {
		return true
	}

	// The plugins tagged their roles without the UID before they bound a role
	// to its request, so such a role is taken over once.
	return live[TagRequestUID] == "" &&
		live[TagNamespace] != "" && live[TagNamespace] == want[TagNamespace] &&
		live[TagRequest] != "" && live[TagRequest] == want[TagRequest]
}

// ownerOf names the request of the tags of a role, for a RoleInUseError.
func ownerOf(tags map[string]string) string {
	if tags[TagRequest] == "" {
		return "no request"
	}

	owner := "the request " + tags[TagNamespace] + "/" + tags[TagRequest]
	if uid := tags[TagRequestUID]; uid != "" {
		owner += " with the UID " + uid
	}
	return owner
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

// updateRole brings the live role, with the tags liveTags, to spec and
// returns the parts that it updated.
func updateRole(
	ctx context.Context,
	c IAM,
	live *types.Role,
	liveTags map[string]string,
	spec RoleSpec,
) ([]string, error) {
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

// MarkDesired tags the role with tags and TagDesired with hash in one
// TagRole call, when role.Desired is not hash. It reports whether it tagged.
// A plugin calls it after every step of a pass succeeded, so that a retry of
// a change is not drift.
func MarkDesired(ctx context.Context, c IAM, role Role, tags map[string]string, hash string) (bool, error) {
	if role.Desired == hash {
		return false, nil
	}

	tags = maps.Clone(tags)
	if tags == nil {
		tags = map[string]string{}
	}
	tags[TagDesired] = hash
	if _, err := c.TagRole(ctx, &iam.TagRoleInput{RoleName: aws.String(role.Name), Tags: iamTags(tags)}); err != nil {
		return false, fmt.Errorf("tagging the IAM role %s with its desired state: %w", role.Name, err)
	}
	return true, nil
}

// ClaimRole returns the Role of name when it belongs to the request of tags,
// and a zero Role when the role does not exist. A role belongs to the request
// when its TagRequestUID value is the one in tags, or when EnsureRole would
// take it over. In the second case ClaimRole writes tags on the role. It
// returns a RoleInUseError for any other role.
func ClaimRole(ctx context.Context, c IAM, name string, tags map[string]string) (Role, error) {
	out, err := c.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(name)})
	if IsNotFound(err) {
		return Role{}, nil
	}
	if err != nil {
		return Role{}, fmt.Errorf("reading the IAM role %s: %w", name, err)
	}

	live := roleTags(out.Role)
	if !ownedBy(live, tags) {
		return Role{}, RoleInUseError{Name: name, Owner: ownerOf(live)}
	}

	if live[TagRequestUID] != tags[TagRequestUID] {
		if _, err := c.TagRole(ctx, &iam.TagRoleInput{RoleName: aws.String(name), Tags: iamTags(tags)}); err != nil {
			return Role{}, fmt.Errorf("tagging the IAM role %s: %w", name, err)
		}
	}
	return Role{Name: name, ARN: aws.ToString(out.Role.Arn), Desired: live[TagDesired]}, nil
}

// ErrTagNotVisible means that a read of IAM did not show the TagRequestUID
// tag that ClaimRole wrote just before. IAM is eventually consistent, so a
// later call deletes the role.
var ErrTagNotVisible = errors.New("IAM does not show the UID tag of the request on the role yet")

// DeleteClaimedRole claims the role name for the request of tags with
// ClaimRole, then deletes it with DeleteRole. It reports false, with no
// error, when the role does not exist, a RoleInUseError for a role of another
// request, and ErrTagNotVisible when the read of DeleteRole does not show the
// UID tag of tags on the role yet.
func DeleteClaimedRole(ctx context.Context, c IAM, name string, tags map[string]string) (bool, error) {
	if _, err := ClaimRole(ctx, c, name, tags); err != nil {
		return false, err
	}

	deleted, err := DeleteRole(ctx, c, name, tags[TagRequestUID])
	if errors.As(err, new(RoleInUseError)) {
		// ClaimRole found the UID of the request on the role, or wrote it.
		return false, fmt.Errorf("deleting the IAM role %s: %w", name, ErrTagNotVisible)
	}
	return deleted, err
}

// DeleteRole deletes the role name with its inline policies and its managed
// policy attachments, only when its TagRequestUID tag is uid. An absent tag
// is the empty value. It reports false, with no error, when the role does not
// exist, and a RoleInUseError when the tag differs from uid.
func DeleteRole(ctx context.Context, c IAM, name, uid string) (bool, error) {
	out, err := c.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(name)})
	if IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reading the IAM role %s: %w", name, err)
	}

	if tags := roleTags(out.Role); tags[TagRequestUID] != uid {
		return false, RoleInUseError{Name: name, Owner: ownerOf(tags)}
	}

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
	// The plugins never put a role into an instance profile, so they leave
	// removing it to the admin.
	if hasCode(err, "DeleteConflict") && strings.Contains(strings.ToLower(Message(err)), "instance profile") {
		return false, fmt.Errorf(
			"deleting the IAM role %s: an instance profile uses it. Remove the role from the instance profile: %w",
			name,
			err,
		)
	}
	if err != nil {
		return false, fmt.Errorf("deleting the IAM role %s: %w", name, err)
	}

	return true, nil
}

// ReplacedRoles returns the names of names that are not current, in any
// case, each once in any case, in the order of names. IAM role names are
// unique without regard to case.
func ReplacedRoles(current string, names []string) []string {
	var replaced []string
	for _, name := range names {
		same := func(other string) bool { return strings.EqualFold(other, name) }
		if !same(current) && !slices.ContainsFunc(replaced, same) {
			replaced = append(replaced, name)
		}
	}
	return replaced
}

// PolicyChange names the managed policies that EnsureAttachedPolicies
// attached and detached, each sorted.
type PolicyChange struct {
	Attached, Detached []string
}

// Corrections names each managed policy of the role name that c attached or
// detached, for pluginkit.Result.Corrected.
func (c PolicyChange) Corrections(name string) []string {
	arns := slices.Concat(c.Attached, c.Detached)
	out := make([]string, 0, len(arns))
	for _, arn := range arns {
		out = append(out, "the managed policy "+arn+" of the IAM role "+name)
	}
	return out
}

// InlinePolicyCorrections names each inline policy of policies on the role
// name, for pluginkit.Result.Corrected.
func InlinePolicyCorrections(name string, policies ...string) []string {
	out := make([]string, 0, len(policies))
	for _, policy := range policies {
		out = append(out, "the inline policy "+policy+" of the IAM role "+name)
	}
	return out
}

// EnsureAttachedPolicies makes the managed policies of the role exactly
// arns, which may hold an ARN twice. It detaches each other policy before it
// attaches a missing one, so a swap does not hit the quota of managed
// policies on a role. An error keeps its AWS code, for IsLimitExceeded and
// IsNotFound.
func EnsureAttachedPolicies(ctx context.Context, c IAM, role string, arns []string) (PolicyChange, error) {
	var change PolicyChange
	live, err := attachedPolicies(ctx, c, role)
	if err != nil {
		return change, err
	}

	for _, arn := range live {
		if slices.Contains(arns, arn) {
			continue
		}
		_, err := c.DetachRolePolicy(
			ctx,
			&iam.DetachRolePolicyInput{RoleName: aws.String(role), PolicyArn: aws.String(arn)},
		)
		if err != nil && !IsNotFound(err) {
			return change, fmt.Errorf("detaching the policy %s from the IAM role %s: %w", arn, role, err)
		}
		change.Detached = append(change.Detached, arn)
	}

	wanted := slices.Compact(slices.Sorted(slices.Values(arns)))
	for _, arn := range wanted {
		if slices.Contains(live, arn) {
			continue
		}
		_, err := c.AttachRolePolicy(
			ctx,
			&iam.AttachRolePolicyInput{RoleName: aws.String(role), PolicyArn: aws.String(arn)},
		)
		if err != nil {
			return change, fmt.Errorf("attaching the policy %s to the IAM role %s: %w", arn, role, err)
		}
		change.Attached = append(change.Attached, arn)
	}

	slices.Sort(change.Detached)
	return change, nil
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
