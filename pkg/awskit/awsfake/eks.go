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
	"maps"
	"slices"
	"strconv"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
)

var _ awskit.EKS = (*EKS)(nil)

// EKS is an in-memory EKS with Pod Identity associations.
type EKS struct {
	// PageSize is the number of associations of one page of a list. Zero
	// puts every association in one page.
	PageSize int

	mu           sync.Mutex
	associations []*Association
	ids          int
	fail         failures
}

// Association is a Pod Identity association of the fake.
type Association struct {
	ID, Cluster, Namespace, ServiceAccount, RoleARN string
	Tags                                            map[string]string
}

// NewEKS returns an EKS without associations.
func NewEKS() *EKS {
	return &EKS{}
}

// Associations returns a copy of each association, in create order.
func (f *EKS) Associations() []Association {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Association, 0, len(f.associations))
	for _, a := range f.associations {
		c := *a
		c.Tags = maps.Clone(a.Tags)
		out = append(out, c)
	}
	return out
}

// FailNext makes the next call of operation return err.
func (f *EKS) FailNext(operation string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail.next(operation, err)
}

// Calls returns the names of the operations called, in call order.
func (f *EKS) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.fail.calls)
}

func (f *EKS) find(cluster, id *string) (*Association, error) {
	for _, a := range f.associations {
		if a.Cluster == aws.ToString(cluster) && a.ID == aws.ToString(id) {
			return a, nil
		}
	}
	return nil, apiError("ResourceNotFoundException", "The association %s was not found.", aws.ToString(id))
}

func (a *Association) describe() *types.PodIdentityAssociation {
	return &types.PodIdentityAssociation{
		AssociationId:  aws.String(a.ID),
		ClusterName:    aws.String(a.Cluster),
		Namespace:      aws.String(a.Namespace),
		ServiceAccount: aws.String(a.ServiceAccount),
		RoleArn:        aws.String(a.RoleARN),
		Tags:           maps.Clone(a.Tags),
	}
}

// CreatePodIdentityAssociation creates an association. It refuses a second
// association for one namespace and ServiceAccount, with
// ResourceInUseException.
func (f *EKS) CreatePodIdentityAssociation(
	_ context.Context,
	in *eks.CreatePodIdentityAssociationInput,
	_ ...func(*eks.Options),
) (*eks.CreatePodIdentityAssociationOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail.call("CreatePodIdentityAssociation"); err != nil {
		return nil, err
	}

	for _, a := range f.associations {
		if a.Cluster == aws.ToString(in.ClusterName) && a.Namespace == aws.ToString(in.Namespace) &&
			a.ServiceAccount == aws.ToString(in.ServiceAccount) {
			return nil, apiError(
				"ResourceInUseException",
				"Association already exists: %s",
				a.ID,
			)
		}
	}

	f.ids++
	a := &Association{
		ID:             "a-" + strconv.Itoa(f.ids),
		Cluster:        aws.ToString(in.ClusterName),
		Namespace:      aws.ToString(in.Namespace),
		ServiceAccount: aws.ToString(in.ServiceAccount),
		RoleARN:        aws.ToString(in.RoleArn),
		Tags:           maps.Clone(in.Tags),
	}
	f.associations = append(f.associations, a)

	return &eks.CreatePodIdentityAssociationOutput{Association: a.describe()}, nil
}

// ListPodIdentityAssociations returns one page of the associations of a
// cluster, filtered by namespace and ServiceAccount when the input sets them.
// The summaries carry no role ARN and no tags.
func (f *EKS) ListPodIdentityAssociations(
	_ context.Context,
	in *eks.ListPodIdentityAssociationsInput,
	_ ...func(*eks.Options),
) (*eks.ListPodIdentityAssociationsOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail.call("ListPodIdentityAssociations"); err != nil {
		return nil, err
	}

	var matching []*Association
	for _, a := range f.associations {
		if a.Cluster != aws.ToString(in.ClusterName) ||
			(in.Namespace != nil && a.Namespace != *in.Namespace) ||
			(in.ServiceAccount != nil && a.ServiceAccount != *in.ServiceAccount) {
			continue
		}
		matching = append(matching, a)
	}

	items, next, err := page(matching, in.NextToken, f.PageSize)
	if err != nil {
		return nil, err
	}

	out := &eks.ListPodIdentityAssociationsOutput{NextToken: next}
	for _, a := range items {
		out.Associations = append(out.Associations, types.PodIdentityAssociationSummary{
			AssociationId:  aws.String(a.ID),
			ClusterName:    aws.String(a.Cluster),
			Namespace:      aws.String(a.Namespace),
			ServiceAccount: aws.String(a.ServiceAccount),
		})
	}
	return out, nil
}

// DescribePodIdentityAssociation returns an association with its role and
// tags.
func (f *EKS) DescribePodIdentityAssociation(
	_ context.Context,
	in *eks.DescribePodIdentityAssociationInput,
	_ ...func(*eks.Options),
) (*eks.DescribePodIdentityAssociationOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail.call("DescribePodIdentityAssociation"); err != nil {
		return nil, err
	}

	a, err := f.find(in.ClusterName, in.AssociationId)
	if err != nil {
		return nil, err
	}
	return &eks.DescribePodIdentityAssociationOutput{Association: a.describe()}, nil
}

// UpdatePodIdentityAssociation sets the role of an association.
func (f *EKS) UpdatePodIdentityAssociation(
	_ context.Context,
	in *eks.UpdatePodIdentityAssociationInput,
	_ ...func(*eks.Options),
) (*eks.UpdatePodIdentityAssociationOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail.call("UpdatePodIdentityAssociation"); err != nil {
		return nil, err
	}

	a, err := f.find(in.ClusterName, in.AssociationId)
	if err != nil {
		return nil, err
	}

	if in.RoleArn != nil {
		a.RoleARN = *in.RoleArn
	}
	return &eks.UpdatePodIdentityAssociationOutput{Association: a.describe()}, nil
}

// DeletePodIdentityAssociation deletes an association.
func (f *EKS) DeletePodIdentityAssociation(
	_ context.Context,
	in *eks.DeletePodIdentityAssociationInput,
	_ ...func(*eks.Options),
) (*eks.DeletePodIdentityAssociationOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail.call("DeletePodIdentityAssociation"); err != nil {
		return nil, err
	}

	a, err := f.find(in.ClusterName, in.AssociationId)
	if err != nil {
		return nil, err
	}

	f.associations = slices.DeleteFunc(f.associations, func(o *Association) bool { return o == a })
	return &eks.DeletePodIdentityAssociationOutput{Association: a.describe()}, nil
}
