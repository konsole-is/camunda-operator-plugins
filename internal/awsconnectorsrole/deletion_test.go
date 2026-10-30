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

package awsconnectorsrole_test

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"

	"github.com/konsole-is/camunda-operator-plugins/internal/awsconnectorsrole"
	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
)

// paramsOf returns parameters of the registration of c with one managed
// policy and an inline policy, or the existing role.
func paramsOf(c *connectors) string {
	if c.registration == awsconnectorsrole.RegistrationExistingRole {
		return existingParams(existingRoleARN)
	}
	return `{"managedPolicyArns":["` + policyA + `"],"inlinePolicy":` + sqsPolicy + `}`
}

var _ = Describe("the deletion of a request", func() {
	DescribeTable(
		"leaves the expected AWS objects",
		func(c func() *connectors, policy pluginsv1alpha1.DeletionPolicy, wantRole bool) {
			h := c()
			ns := newNamespace(h)
			req := connectorsRequest(h, ns, paramsOf(h))
			req.Spec.DeletionPolicy = policy
			Expect(h.k8s.Create(ctx, req)).To(Succeed())
			waitReady(h, req)

			waitGone(h, req)

			if h.registration == awsconnectorsrole.RegistrationRole {
				iamRole, ok := h.iam.Role(h.roleName(ns))
				Expect(ok).To(Equal(wantRole))
				if ok {
					Expect(iamRole.Attached).To(ConsistOf(policyA))
					Expect(iamRole.InlinePolicies).To(HaveKey("connectors"))
				}
			} else {
				Expect(h.iam.Calls()).To(BeEmpty())
			}
			Expect(associationsIn(h, ns)).To(BeEmpty())
		},
		Entry("role, IRSA, Delete", func() *connectors { return role }, pluginsv1alpha1.DeletionPolicyDelete, false),
		Entry("role, IRSA, Orphan", func() *connectors { return role }, pluginsv1alpha1.DeletionPolicyOrphan, true),
		Entry(
			"role, Pod Identity, Delete",
			func() *connectors { return rolePodIdentity },
			pluginsv1alpha1.DeletionPolicyDelete,
			false,
		),
		Entry(
			"role, Pod Identity, Orphan",
			func() *connectors { return rolePodIdentity },
			pluginsv1alpha1.DeletionPolicyOrphan,
			true,
		),
		Entry(
			"existing, IRSA, Delete",
			func() *connectors { return existing },
			pluginsv1alpha1.DeletionPolicyDelete,
			false,
		),
		Entry(
			"existing, IRSA, Orphan",
			func() *connectors { return existing },
			pluginsv1alpha1.DeletionPolicyOrphan,
			false,
		),
		Entry(
			"existing, Pod Identity, Delete",
			func() *connectors { return existingPodIdentity },
			pluginsv1alpha1.DeletionPolicyDelete,
			false,
		),
		Entry(
			"existing, Pod Identity, Orphan",
			func() *connectors { return existingPodIdentity },
			pluginsv1alpha1.DeletionPolicyOrphan,
			false,
		),
	)

	It("orphans an IRSA request without an IAM call", func() {
		ns := newNamespace(role)
		req := connectorsRequest(role, ns, paramsOf(role))
		req.Spec.DeletionPolicy = pluginsv1alpha1.DeletionPolicyOrphan
		Expect(role.k8s.Create(ctx, req)).To(Succeed())
		waitReady(role, req)
		before := len(iamCallsBut(role, "GetPolicy"))

		waitGone(role, req)

		Expect(iamCallsBut(role, "GetPolicy")[before:]).To(BeEmpty())
	})

	It("releases the request when its role is already gone", func() {
		ns := newNamespace(role)
		req := connectorsRequest(role, ns, paramsOf(role))
		Expect(role.k8s.Create(ctx, req)).To(Succeed())
		waitReady(role, req)

		deleted, err := awskit.DeleteRole(context.Background(), role.iam, role.roleName(ns))
		Expect(err).NotTo(HaveOccurred())
		Expect(deleted).To(BeTrue())

		waitGone(role, req)
	})

	It("deletes the association of a role that someone deleted by hand", func() {
		ns := newNamespace(rolePodIdentity)
		req := connectorsRequest(rolePodIdentity, ns, `{}`)
		Expect(rolePodIdentity.k8s.Create(ctx, req)).To(Succeed())
		waitReady(rolePodIdentity, req)
		Expect(associationsIn(rolePodIdentity, ns)).To(HaveLen(1))

		_, err := rolePodIdentity.iam.DeleteRole(context.Background(), &iam.DeleteRoleInput{
			RoleName: aws.String(rolePodIdentity.roleName(ns)),
		})
		Expect(err).NotTo(HaveOccurred())

		waitGone(rolePodIdentity, req)
		Expect(associationsIn(rolePodIdentity, ns)).To(BeEmpty())
	})
})
