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

package awsfake

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strconv"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/smithy-go"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
)

// DefaultAccount is the account of a fake that sets none.
const DefaultAccount = "111122223333"

var _ awskit.IAM = (*IAM)(nil)

// IAM is an in-memory IAM.
type IAM struct {
	// PageSize is the number of items of one page of a list. Zero puts every
	// item in one page.
	PageSize int
	// Account is the account of the ARNs. Empty is DefaultAccount.
	Account string

	mu       sync.Mutex
	roles    map[string]*Role
	policies map[string]bool
	fail     failures
}

// Role is a role of the fake, with its documents decoded.
type Role struct {
	Name, ARN, Description, TrustPolicy, PermissionsBoundary string
	Tags                                                     map[string]string
	// InlinePolicies maps a policy name to its document.
	InlinePolicies map[string]string
	// Attached holds the ARNs of the managed policies of the role.
	Attached []string
}

// NewIAM returns an IAM without roles and without managed policies.
func NewIAM() *IAM {
	return &IAM{roles: map[string]*Role{}, policies: map[string]bool{}}
}

// AddPolicy adds a managed policy that GetPolicy finds, such as a
// permissions boundary.
func (f *IAM) AddPolicy(arn string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.policies[arn] = true
}

// AttachPolicy attaches the managed policy arn to the role name, as an admin
// does by hand.
func (f *IAM) AttachPolicy(name, arn string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, ok := f.roles[name]; ok {
		r.Attached = append(r.Attached, arn)
	}
}

// Role returns a copy of the role name.
func (f *IAM) Role(name string) (Role, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.roles[name]
	if !ok {
		return Role{}, false
	}
	return r.clone(), true
}

// Roles returns a copy of each role, sorted by name.
func (f *IAM) Roles() []Role {
	f.mu.Lock()
	defer f.mu.Unlock()
	roles := make([]Role, 0, len(f.roles))
	for _, name := range slices.Sorted(maps.Keys(f.roles)) {
		roles = append(roles, f.roles[name].clone())
	}
	return roles
}

func (r *Role) clone() Role {
	c := *r
	c.Tags = maps.Clone(r.Tags)
	c.InlinePolicies = maps.Clone(r.InlinePolicies)
	c.Attached = slices.Clone(r.Attached)
	return c
}

// FailNext makes the next call of operation, such as "PutRolePolicy", return
// err.
func (f *IAM) FailNext(operation string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail.next(operation, err)
}

// Calls returns the names of the operations called, in call order.
func (f *IAM) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.fail.calls)
}

func (f *IAM) role(name *string) (*Role, error) {
	r, ok := f.roles[aws.ToString(name)]
	if !ok {
		return nil, apiError("NoSuchEntity", "The role with name %s cannot be found.", aws.ToString(name))
	}
	return r, nil
}

func (f *IAM) arn(kind, name string) string {
	account := f.Account
	if account == "" {
		account = DefaultAccount
	}
	return "arn:aws:iam::" + account + ":" + kind + "/" + name
}

// GetRole returns the role with its trust policy URL-encoded.
func (f *IAM) GetRole(_ context.Context, in *iam.GetRoleInput, _ ...func(*iam.Options)) (*iam.GetRoleOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail.call("GetRole"); err != nil {
		return nil, err
	}

	r, err := f.role(in.RoleName)
	if err != nil {
		return nil, err
	}

	out := &types.Role{
		RoleName:                 aws.String(r.Name),
		Arn:                      aws.String(r.ARN),
		Description:              aws.String(r.Description),
		AssumeRolePolicyDocument: aws.String(url.QueryEscape(r.TrustPolicy)),
	}
	if r.PermissionsBoundary != "" {
		out.PermissionsBoundary = &types.AttachedPermissionsBoundary{
			PermissionsBoundaryArn: aws.String(r.PermissionsBoundary),
		}
	}
	for _, k := range slices.Sorted(maps.Keys(r.Tags)) {
		out.Tags = append(out.Tags, types.Tag{Key: aws.String(k), Value: aws.String(r.Tags[k])})
	}
	return &iam.GetRoleOutput{Role: out}, nil
}

// CreateRole creates a role. It refuses a name that exists, and a trust policy
// that is not JSON.
func (f *IAM) CreateRole(
	_ context.Context,
	in *iam.CreateRoleInput,
	_ ...func(*iam.Options),
) (*iam.CreateRoleOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail.call("CreateRole"); err != nil {
		return nil, err
	}

	name := aws.ToString(in.RoleName)
	if _, ok := f.roles[name]; ok {
		return nil, apiError("EntityAlreadyExists", "Role with name %s already exists.", name)
	}

	if !json.Valid([]byte(aws.ToString(in.AssumeRolePolicyDocument))) {
		return nil, apiError("MalformedPolicyDocument", "The trust policy is not JSON.")
	}

	r := &Role{
		Name:                name,
		ARN:                 f.arn("role", name),
		Description:         aws.ToString(in.Description),
		TrustPolicy:         aws.ToString(in.AssumeRolePolicyDocument),
		PermissionsBoundary: aws.ToString(in.PermissionsBoundary),
		Tags:                map[string]string{},
		InlinePolicies:      map[string]string{},
	}
	for _, t := range in.Tags {
		r.Tags[aws.ToString(t.Key)] = aws.ToString(t.Value)
	}
	f.roles[name] = r

	return &iam.CreateRoleOutput{Role: &types.Role{RoleName: aws.String(name), Arn: aws.String(r.ARN)}}, nil
}

// UpdateAssumeRolePolicy replaces the trust policy of a role.
func (f *IAM) UpdateAssumeRolePolicy(
	_ context.Context,
	in *iam.UpdateAssumeRolePolicyInput,
	_ ...func(*iam.Options),
) (*iam.UpdateAssumeRolePolicyOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail.call("UpdateAssumeRolePolicy"); err != nil {
		return nil, err
	}

	r, err := f.role(in.RoleName)
	if err != nil {
		return nil, err
	}

	r.TrustPolicy = aws.ToString(in.PolicyDocument)
	return &iam.UpdateAssumeRolePolicyOutput{}, nil
}

// PutRolePermissionsBoundary sets the permissions boundary of a role.
func (f *IAM) PutRolePermissionsBoundary(
	_ context.Context,
	in *iam.PutRolePermissionsBoundaryInput,
	_ ...func(*iam.Options),
) (*iam.PutRolePermissionsBoundaryOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail.call("PutRolePermissionsBoundary"); err != nil {
		return nil, err
	}

	r, err := f.role(in.RoleName)
	if err != nil {
		return nil, err
	}

	r.PermissionsBoundary = aws.ToString(in.PermissionsBoundary)
	return &iam.PutRolePermissionsBoundaryOutput{}, nil
}

// TagRole adds or replaces tags of a role.
func (f *IAM) TagRole(_ context.Context, in *iam.TagRoleInput, _ ...func(*iam.Options)) (*iam.TagRoleOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail.call("TagRole"); err != nil {
		return nil, err
	}

	r, err := f.role(in.RoleName)
	if err != nil {
		return nil, err
	}

	for _, t := range in.Tags {
		r.Tags[aws.ToString(t.Key)] = aws.ToString(t.Value)
	}
	return &iam.TagRoleOutput{}, nil
}

// DeleteRole deletes a role. It refuses a role that has an inline or an
// attached policy, with DeleteConflict.
func (f *IAM) DeleteRole(
	_ context.Context,
	in *iam.DeleteRoleInput,
	_ ...func(*iam.Options),
) (*iam.DeleteRoleOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail.call("DeleteRole"); err != nil {
		return nil, err
	}

	r, err := f.role(in.RoleName)
	if err != nil {
		return nil, err
	}

	if len(r.InlinePolicies) > 0 || len(r.Attached) > 0 {
		return nil, apiError("DeleteConflict", "Cannot delete entity, must delete policies first.")
	}

	delete(f.roles, r.Name)
	return &iam.DeleteRoleOutput{}, nil
}

// PutRolePolicy adds or replaces an inline policy. It refuses a document that
// is not JSON, and one that takes the inline policies of the role over
// awskit.MaxInlinePolicySize.
func (f *IAM) PutRolePolicy(
	_ context.Context,
	in *iam.PutRolePolicyInput,
	_ ...func(*iam.Options),
) (*iam.PutRolePolicyOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail.call("PutRolePolicy"); err != nil {
		return nil, err
	}

	r, err := f.role(in.RoleName)
	if err != nil {
		return nil, err
	}

	name, doc := aws.ToString(in.PolicyName), aws.ToString(in.PolicyDocument)
	if !json.Valid([]byte(doc)) {
		return nil, apiError("MalformedPolicyDocument", "Syntax errors in policy.")
	}

	size := awskit.PolicySize([]byte(doc))
	for other, d := range r.InlinePolicies {
		if other != name {
			size += awskit.PolicySize([]byte(d))
		}
	}
	if size > awskit.MaxInlinePolicySize {
		return nil, apiError(
			"LimitExceeded",
			"Maximum policy size of %d bytes exceeded for role %s",
			awskit.MaxInlinePolicySize,
			r.Name,
		)
	}

	r.InlinePolicies[name] = doc
	return &iam.PutRolePolicyOutput{}, nil
}

// GetRolePolicy returns an inline policy with its document URL-encoded.
func (f *IAM) GetRolePolicy(
	_ context.Context,
	in *iam.GetRolePolicyInput,
	_ ...func(*iam.Options),
) (*iam.GetRolePolicyOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail.call("GetRolePolicy"); err != nil {
		return nil, err
	}

	r, err := f.role(in.RoleName)
	if err != nil {
		return nil, err
	}

	doc, ok := r.InlinePolicies[aws.ToString(in.PolicyName)]
	if !ok {
		return nil, apiError(
			"NoSuchEntity",
			"The role policy with name %s cannot be found.",
			aws.ToString(in.PolicyName),
		)
	}

	return &iam.GetRolePolicyOutput{
		RoleName:       in.RoleName,
		PolicyName:     in.PolicyName,
		PolicyDocument: aws.String(url.QueryEscape(doc)),
	}, nil
}

// ListRolePolicies returns one page of the inline policy names of a role,
// sorted.
func (f *IAM) ListRolePolicies(
	_ context.Context,
	in *iam.ListRolePoliciesInput,
	_ ...func(*iam.Options),
) (*iam.ListRolePoliciesOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail.call("ListRolePolicies"); err != nil {
		return nil, err
	}

	r, err := f.role(in.RoleName)
	if err != nil {
		return nil, err
	}

	names, next, err := page(slices.Sorted(maps.Keys(r.InlinePolicies)), in.Marker, f.PageSize)
	if err != nil {
		return nil, err
	}
	return &iam.ListRolePoliciesOutput{PolicyNames: names, IsTruncated: next != nil, Marker: next}, nil
}

// DeleteRolePolicy deletes an inline policy.
func (f *IAM) DeleteRolePolicy(
	_ context.Context,
	in *iam.DeleteRolePolicyInput,
	_ ...func(*iam.Options),
) (*iam.DeleteRolePolicyOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail.call("DeleteRolePolicy"); err != nil {
		return nil, err
	}

	r, err := f.role(in.RoleName)
	if err != nil {
		return nil, err
	}

	name := aws.ToString(in.PolicyName)
	if _, ok := r.InlinePolicies[name]; !ok {
		return nil, apiError("NoSuchEntity", "The role policy with name %s cannot be found.", name)
	}

	delete(r.InlinePolicies, name)
	return &iam.DeleteRolePolicyOutput{}, nil
}

// ListAttachedRolePolicies returns one page of the managed policies of a
// role, in attach order.
func (f *IAM) ListAttachedRolePolicies(
	_ context.Context,
	in *iam.ListAttachedRolePoliciesInput,
	_ ...func(*iam.Options),
) (*iam.ListAttachedRolePoliciesOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail.call("ListAttachedRolePolicies"); err != nil {
		return nil, err
	}

	r, err := f.role(in.RoleName)
	if err != nil {
		return nil, err
	}

	arns, next, err := page(r.Attached, in.Marker, f.PageSize)
	if err != nil {
		return nil, err
	}

	out := &iam.ListAttachedRolePoliciesOutput{IsTruncated: next != nil, Marker: next}
	for _, arn := range arns {
		out.AttachedPolicies = append(out.AttachedPolicies, types.AttachedPolicy{PolicyArn: aws.String(arn)})
	}
	return out, nil
}

// AttachRolePolicy attaches a managed policy that AddPolicy added.
func (f *IAM) AttachRolePolicy(
	_ context.Context,
	in *iam.AttachRolePolicyInput,
	_ ...func(*iam.Options),
) (*iam.AttachRolePolicyOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail.call("AttachRolePolicy"); err != nil {
		return nil, err
	}

	r, err := f.role(in.RoleName)
	if err != nil {
		return nil, err
	}

	arn := aws.ToString(in.PolicyArn)
	if !f.policies[arn] {
		return nil, apiError("NoSuchEntity", "Policy %s does not exist or is not attachable.", arn)
	}

	if !slices.Contains(r.Attached, arn) {
		r.Attached = append(r.Attached, arn)
	}
	return &iam.AttachRolePolicyOutput{}, nil
}

// DetachRolePolicy detaches a managed policy.
func (f *IAM) DetachRolePolicy(
	_ context.Context,
	in *iam.DetachRolePolicyInput,
	_ ...func(*iam.Options),
) (*iam.DetachRolePolicyOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail.call("DetachRolePolicy"); err != nil {
		return nil, err
	}

	r, err := f.role(in.RoleName)
	if err != nil {
		return nil, err
	}

	i := slices.Index(r.Attached, aws.ToString(in.PolicyArn))
	if i == -1 {
		return nil, apiError("NoSuchEntity", "Policy %s was not found.", aws.ToString(in.PolicyArn))
	}

	r.Attached = slices.Delete(r.Attached, i, i+1)
	return &iam.DetachRolePolicyOutput{}, nil
}

// GetPolicy returns a managed policy that AddPolicy added.
func (f *IAM) GetPolicy(
	_ context.Context,
	in *iam.GetPolicyInput,
	_ ...func(*iam.Options),
) (*iam.GetPolicyOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail.call("GetPolicy"); err != nil {
		return nil, err
	}

	arn := aws.ToString(in.PolicyArn)
	if !f.policies[arn] {
		return nil, apiError("NoSuchEntity", "Policy %s was not found.", arn)
	}
	return &iam.GetPolicyOutput{Policy: &types.Policy{Arn: aws.String(arn)}}, nil
}

// failures records the calls of a fake and the errors of FailNext.
type failures struct {
	calls   []string
	pending map[string][]error
}

func (f *failures) next(operation string, err error) {
	if f.pending == nil {
		f.pending = map[string][]error{}
	}
	f.pending[operation] = append(f.pending[operation], err)
}

func (f *failures) call(operation string) error {
	f.calls = append(f.calls, operation)
	errs := f.pending[operation]
	if len(errs) == 0 {
		return nil
	}
	f.pending[operation] = errs[1:]
	return errs[0]
}

// page returns the page of items that starts at marker, and the marker of the
// next page, or nil on the last page. A marker is the index of the first item
// of its page.
func page[T any](items []T, marker *string, size int) ([]T, *string, error) {
	start := 0
	if marker != nil {
		var err error
		if start, err = strconv.Atoi(*marker); err != nil || start < 0 || start > len(items) {
			return nil, nil, apiError("InvalidInput", "The marker %q is not valid.", *marker)
		}
	}

	if size <= 0 || start+size >= len(items) {
		return slices.Clone(items[start:]), nil, nil
	}
	return slices.Clone(items[start : start+size]), aws.String(strconv.Itoa(start + size)), nil
}

func apiError(code, format string, args ...any) error {
	return &smithy.GenericAPIError{Code: code, Message: fmt.Sprintf(format, args...)}
}
