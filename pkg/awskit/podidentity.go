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
	"cmp"
	"context"
	"fmt"
	"slices"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
)

// Association is a Pod Identity association of one ServiceAccount.
type Association struct {
	ID, ServiceAccount, RoleARN string
}

// AssociationChange names the ServiceAccounts whose association
// EnsureAssociations created, moved to the role, or deleted.
type AssociationChange struct {
	Created, Updated, Deleted []string
}

// EnsureAssociations makes one association of roleARN for each of
// serviceAccounts in namespace of the EKS cluster, with tags, and returns
// them sorted by ServiceAccount. EKS allows one association for each
// ServiceAccount. It moves an association of another role only when the
// association carries the TagNamespace and TagClaim values of tags, and else
// refuses with an error that names the ServiceAccount and that role. It
// deletes each association of roleARN in namespace whose ServiceAccount is
// not in serviceAccounts.
func EnsureAssociations(
	ctx context.Context,
	c EKS,
	cluster, namespace string,
	serviceAccounts []string,
	roleARN string,
	tags map[string]string,
) ([]Association, AssociationChange, error) {
	var change AssociationChange
	live, err := associations(ctx, c, cluster, namespace)
	if err != nil {
		return nil, change, err
	}

	var result []Association
	for _, sa := range serviceAccounts {
		i := slices.IndexFunc(live, func(a liveAssociation) bool { return a.ServiceAccount == sa })
		switch {
		case i == -1:
			out, err := c.CreatePodIdentityAssociation(ctx, &eks.CreatePodIdentityAssociationInput{
				ClusterName:    aws.String(cluster),
				Namespace:      aws.String(namespace),
				ServiceAccount: aws.String(sa),
				RoleArn:        aws.String(roleARN),
				Tags:           tags,
			})
			if err != nil {
				return nil, change, fmt.Errorf("creating the Pod Identity association of %s/%s: %w", namespace, sa, err)
			}
			result = append(
				result,
				Association{ID: aws.ToString(out.Association.AssociationId), ServiceAccount: sa, RoleARN: roleARN},
			)
			change.Created = append(change.Created, sa)
		case live[i].RoleARN == roleARN:
			result = append(result, live[i].Association)
		case live[i].tags[TagNamespace] == tags[TagNamespace] && live[i].tags[TagClaim] == tags[TagClaim]:
			_, err := c.UpdatePodIdentityAssociation(ctx, &eks.UpdatePodIdentityAssociationInput{
				ClusterName:   aws.String(cluster),
				AssociationId: aws.String(live[i].ID),
				RoleArn:       aws.String(roleARN),
			})
			if err != nil {
				return nil, change, fmt.Errorf("moving the Pod Identity association of %s/%s: %w", namespace, sa, err)
			}
			result = append(result, Association{ID: live[i].ID, ServiceAccount: sa, RoleARN: roleARN})
			change.Updated = append(change.Updated, sa)
		default:
			return nil, change, fmt.Errorf(
				"the ServiceAccount %s/%s has the Pod Identity association %s for the role %s, "+
					"which belongs to another claim",
				namespace,
				sa,
				live[i].ID,
				live[i].RoleARN,
			)
		}
	}

	for _, a := range live {
		if a.RoleARN != roleARN || slices.Contains(serviceAccounts, a.ServiceAccount) {
			continue
		}
		if err := deleteAssociation(ctx, c, cluster, namespace, a.Association); err != nil {
			return nil, change, err
		}
		change.Deleted = append(change.Deleted, a.ServiceAccount)
	}

	slices.SortFunc(result, func(a, b Association) int { return cmp.Compare(a.ServiceAccount, b.ServiceAccount) })
	return result, change, nil
}

// DeleteAssociations deletes each association of roleARN in namespace of the
// EKS cluster and returns their ServiceAccounts, sorted.
func DeleteAssociations(ctx context.Context, c EKS, cluster, namespace, roleARN string) ([]string, error) {
	live, err := associations(ctx, c, cluster, namespace)
	if err != nil {
		return nil, err
	}

	var deleted []string
	for _, a := range live {
		if a.RoleARN != roleARN {
			continue
		}
		if err := deleteAssociation(ctx, c, cluster, namespace, a.Association); err != nil {
			return nil, err
		}
		deleted = append(deleted, a.ServiceAccount)
	}

	slices.Sort(deleted)
	return deleted, nil
}

type liveAssociation struct {
	Association
	tags map[string]string
}

// associations returns each association in namespace of the cluster, from
// every page, with its role and tags.
func associations(ctx context.Context, c EKS, cluster, namespace string) ([]liveAssociation, error) {
	var live []liveAssociation
	var token *string
	for {
		out, err := c.ListPodIdentityAssociations(ctx, &eks.ListPodIdentityAssociationsInput{
			ClusterName: aws.String(cluster),
			Namespace:   aws.String(namespace),
			NextToken:   token,
		})
		if err != nil {
			return nil, fmt.Errorf("listing the Pod Identity associations of %s: %w", namespace, err)
		}

		for _, s := range out.Associations {
			// The summaries of the list carry no role ARN and no tags.
			d, err := c.DescribePodIdentityAssociation(ctx, &eks.DescribePodIdentityAssociationInput{
				ClusterName:   aws.String(cluster),
				AssociationId: s.AssociationId,
			})
			if IsNotFound(err) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf(
					"reading the Pod Identity association %s: %w",
					aws.ToString(s.AssociationId),
					err,
				)
			}

			live = append(live, liveAssociation{
				Association: Association{
					ID:             aws.ToString(d.Association.AssociationId),
					ServiceAccount: aws.ToString(d.Association.ServiceAccount),
					RoleARN:        aws.ToString(d.Association.RoleArn),
				},
				tags: d.Association.Tags,
			})
		}

		if out.NextToken == nil {
			return live, nil
		}
		token = out.NextToken
	}
}

func deleteAssociation(ctx context.Context, c EKS, cluster, namespace string, a Association) error {
	_, err := c.DeletePodIdentityAssociation(ctx, &eks.DeletePodIdentityAssociationInput{
		ClusterName:   aws.String(cluster),
		AssociationId: aws.String(a.ID),
	})
	if err != nil && !IsNotFound(err) {
		return fmt.Errorf("deleting the Pod Identity association of %s/%s: %w", namespace, a.ServiceAccount, err)
	}
	return nil
}
