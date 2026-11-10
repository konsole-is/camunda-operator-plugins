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
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	eventsv1 "k8s.io/api/events/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
	v1 "github.com/konsole-is/camunda-operator/api/v1"

	"github.com/konsole-is/camunda-operator-plugins/internal/awsconnectorsrole"
	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

// sqsPolicy is an inline policy with the placeholders of a Connectors role.
const sqsPolicy = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:SendMessage",
	"Resource":"arn:aws:sqs:*:*:{{ .namespace }}-{{ .claim }}-*",
	"Condition":{"StringEquals":{"aws:PrincipalTag/sa":"{{ .serviceAccount }}"}}}]}`

func managedParams(arns ...string) string {
	return `{"managedPolicyArns":["` + strings.Join(arns, `","`) + `"]}`
}

// iamCallsBut returns the IAM calls of c without the calls named skip, such
// as the reads of the health check.
func iamCallsBut(c *connectors, skip ...string) []string {
	return slices.DeleteFunc(c.iam.Calls(), func(call string) bool { return slices.Contains(skip, call) })
}

var _ = Describe("the aws-connectors-role registration", func() {
	It("makes one role for a request, trusted by the Connectors ServiceAccount only", func() {
		ns := newNamespace(role)
		req := connectorsRequest(role, ns, `{
			"managedPolicyArns":["`+policyA+`"],
			"extraPolicyArns":["`+policyB+`"],
			"inlinePolicy":`+sqsPolicy+`}`)
		Expect(role.k8s.Create(ctx, req)).To(Succeed())
		got := waitReady(role, req)

		iamRole, ok := role.iam.Role(role.roleName(ns))
		Expect(ok).To(BeTrue())
		Expect(rolesOf(role, ns)).To(ConsistOf(iamRole.Name))
		Expect(iamRole.Name).To(HavePrefix(rolePrefix + "-"))
		Expect(iamRole.TrustPolicy).To(ContainSubstring(`"system:serviceaccount:` + ns + `:orders-connectors"`))
		Expect(iamRole.TrustPolicy).NotTo(ContainSubstring("orders-camunda"))
		Expect(iamRole.PermissionsBoundary).To(Equal(boundary))
		Expect(iamRole.Tags).To(Equal(map[string]string{
			awskit.TagNamespace:  ns,
			awskit.TagClaim:      "orders",
			awskit.TagRequest:    req.Name,
			awskit.TagRequestUID: string(req.UID),
			"cost-center":        "camunda",
		}))
		Expect(iamRole.Attached).To(ConsistOf(policyA, policyB))
		Expect(iamRole.InlinePolicies).To(HaveKey("connectors"))
		Expect(iamRole.InlinePolicies["connectors"]).To(ContainSubstring(`"arn:aws:sqs:*:*:` + ns + `-orders-*"`))
		Expect(iamRole.InlinePolicies["connectors"]).To(ContainSubstring(`"aws:PrincipalTag/sa":"orders-connectors"`))

		Expect(got.Status.Outputs).To(HaveKeyWithValue("principal", iamRole.ARN))
		Expect(got.Status.Outputs["serviceAccountAnnotations"]).
			To(MatchJSON(`{"eks.amazonaws.com/role-arn":"` + iamRole.ARN + `"}`))
		Expect(got.Status.Summary).To(HaveLen(4))
		Expect(got.Status.Summary[0]).To(HaveField("Value", iamRole.Name))
		Expect(got.Status.Summary[1]).To(HaveField("Value", "2"))
		Expect(got.Status.Summary[3]).To(HaveField("Value", boundary))
		Expect(got.Status.Links).To(ConsistOf(HaveField("Name", "IAM role")))
		Expect(got.Status.Resources).To(ConsistOf(And(
			HaveField("Kind", "IAMRole"),
			HaveField("ID", iamRole.ARN),
			HaveField("OnOrphan", pluginsv1alpha1.OnOrphanKept),
		)))
	})

	It("updates the attached policies when managedPolicyArns changes", func() {
		ns := newNamespace(role)
		req := connectorsRequest(role, ns, managedParams(policyA, policyB))
		Expect(role.k8s.Create(ctx, req)).To(Succeed())
		waitReady(role, req)

		update(role, req, func(r *pluginsv1alpha1.PluginRequest) {
			r.Spec.Parameters = raw(managedParams(policyB, policyC))
		})

		Eventually(func(g Gomega) {
			iamRole, ok := role.iam.Role(role.roleName(ns))
			g.Expect(ok).To(BeTrue())
			g.Expect(iamRole.Attached).To(ConsistOf(policyB, policyC))
		}, timeout).Should(Succeed())
		waitReady(role, req)
	})

	It("keeps no attachment that the parameters do not name", func() {
		ns := newNamespace(role)
		req := connectorsRequest(role, ns, managedParams(policyA))
		Expect(role.k8s.Create(ctx, req)).To(Succeed())
		waitReady(role, req)

		role.iam.AttachPolicy(role.roleName(ns), policyC)
		update(role, req, func(r *pluginsv1alpha1.PluginRequest) {
			r.Annotations = map[string]string{"touch": "1"}
		})

		Eventually(func(g Gomega) {
			iamRole, _ := role.iam.Role(role.roleName(ns))
			g.Expect(iamRole.Attached).To(ConsistOf(policyA))
		}, timeout).Should(Succeed())
	})

	It("deletes the inline policy when the parameter is gone", func() {
		ns := newNamespace(role)
		req := connectorsRequest(role, ns, `{"inlinePolicy":`+sqsPolicy+`}`)
		Expect(role.k8s.Create(ctx, req)).To(Succeed())
		waitReady(role, req)
		iamRole, _ := role.iam.Role(role.roleName(ns))
		Expect(iamRole.InlinePolicies).To(HaveKey("connectors"))

		update(role, req, func(r *pluginsv1alpha1.PluginRequest) { r.Spec.Parameters = raw(`{}`) })

		Eventually(func(g Gomega) {
			iamRole, _ := role.iam.Role(role.roleName(ns))
			g.Expect(iamRole.InlinePolicies).To(BeEmpty())
		}, timeout).Should(Succeed())
	})

	It("refuses an inline policy over the size limit and calls no IAM API", func() {
		ns := newNamespace(role)
		before := len(iamCallsBut(role, "GetPolicy"))
		req := connectorsRequest(role, ns, `{"inlinePolicy":{"Version":"2012-10-17","Statement":[{"Sid":"`+
			strings.Repeat("a", 10300)+`","Effect":"Allow","Action":"sqs:SendMessage","Resource":"*"}]}}`)
		Expect(role.k8s.Create(ctx, req)).To(Succeed())

		message := waitReason(role, req, pluginsv1alpha1.ConditionOutputsReady, pluginsv1alpha1.ReasonInvalidParameters)
		Expect(message).To(ContainSubstring("10240"))
		waitReason(role, req, v1.ConditionReady, pluginsv1alpha1.ReasonInvalidParameters)
		Expect(rolesOf(role, ns)).To(BeEmpty())
		// The roles of the other specs exist already, so only this request
		// could create a role or put an inline policy.
		Expect(iamCallsBut(role, "GetPolicy")[before:]).NotTo(ContainElement(Or(
			Equal("CreateRole"), Equal("PutRolePolicy"),
		)))
	})

	It("refuses an inline policy with an unknown placeholder before any IAM call", func() {
		ns := newNamespace(role)
		req := connectorsRequest(role, ns, `{"inlinePolicy":{"Version":"2012-10-17","Statement":[
			{"Effect":"Allow","Action":"sqs:SendMessage","Resource":"arn:aws:sqs:*:*:{{ .cluster }}"}]}}`)
		Expect(role.k8s.Create(ctx, req)).To(Succeed())

		waitReason(role, req, v1.ConditionReady, pluginsv1alpha1.ReasonInvalidParameters)
		Expect(rolesOf(role, ns)).To(BeEmpty())
	})

	It("waits and creates no role when the request names no ServiceAccount", func() {
		ns := newNamespace(role)
		req := connectorsRequest(role, ns, `{}`)
		req.Spec.Target.ServiceAccountName = ""
		Expect(role.k8s.Create(ctx, req)).To(Succeed())

		waitReason(role, req, v1.ConditionReady, "MissingServiceAccount")
		_, ok := role.iam.Role(role.roleName(ns))
		Expect(ok).To(BeFalse())
	})

	It("revokes the trust of a request that names no ServiceAccount any more", func() {
		ns := newNamespace(role)
		req := connectorsRequest(role, ns, managedParams(policyA))
		Expect(role.k8s.Create(ctx, req)).To(Succeed())
		waitReady(role, req)

		update(role, req, func(r *pluginsv1alpha1.PluginRequest) { r.Spec.Target.ServiceAccountName = "" })

		waitReason(role, req, v1.ConditionReady, "MissingServiceAccount")
		iamRole, _ := role.iam.Role(role.roleName(ns))
		Expect(iamRole.TrustPolicy).To(ContainSubstring(`"Effect":"Deny"`))
		Expect(iamRole.TrustPolicy).NotTo(ContainSubstring("orders-connectors"))
	})

	It("deletes the association of a request that names no ServiceAccount any more", func() {
		ns := newNamespace(rolePodIdentity)
		req := connectorsRequest(rolePodIdentity, ns, `{}`)
		Expect(rolePodIdentity.k8s.Create(ctx, req)).To(Succeed())
		waitReady(rolePodIdentity, req)
		Expect(associationsIn(rolePodIdentity, ns)).To(HaveLen(1))

		update(rolePodIdentity, req, func(r *pluginsv1alpha1.PluginRequest) { r.Spec.Target.ServiceAccountName = "" })

		waitReason(rolePodIdentity, req, v1.ConditionReady, "MissingServiceAccount")
		Expect(associationsIn(rolePodIdentity, ns)).To(BeEmpty())
	})

	It("creates one association for the Connectors ServiceAccount in Pod Identity mode", func() {
		ns := newNamespace(rolePodIdentity)
		req := connectorsRequest(rolePodIdentity, ns, managedParams(policyA))
		Expect(rolePodIdentity.k8s.Create(ctx, req)).To(Succeed())
		got := waitReady(rolePodIdentity, req)

		iamRole, ok := rolePodIdentity.iam.Role(rolePodIdentity.roleName(ns))
		Expect(ok).To(BeTrue())
		Expect(iamRole.TrustPolicy).To(ContainSubstring("pods.eks.amazonaws.com"))
		Expect(associationsIn(rolePodIdentity, ns)).To(ConsistOf("orders-connectors=" + iamRole.ARN))
		for _, a := range rolePodIdentity.eks.Associations() {
			if a.Namespace == ns {
				Expect(a.Cluster).To(Equal(eksCluster))
				Expect(a.Tags).To(HaveKeyWithValue("cost-center", "camunda"))
			}
		}
		Expect(got.Status.Outputs["serviceAccountAnnotations"]).To(MatchJSON(`{}`))
		Expect(got.Status.Resources).To(ContainElement(And(
			HaveField("Kind", "PodIdentityAssociation"),
			HaveField("Name", "orders-connectors"),
			HaveField("OnOrphan", pluginsv1alpha1.OnOrphanDeleted),
		)))
	})

	It("deletes the association of a former ServiceAccount when a policy fails to attach", func() {
		ns := newNamespace(rolePodIdentity)
		req := connectorsRequest(rolePodIdentity, ns, managedParams(policyA))
		Expect(rolePodIdentity.k8s.Create(ctx, req)).To(Succeed())
		waitReady(rolePodIdentity, req)

		update(rolePodIdentity, req, func(r *pluginsv1alpha1.PluginRequest) {
			r.Spec.Parameters = raw(managedParams("arn:aws:iam::111122223333:policy/missing"))
			r.Spec.Target.ServiceAccountName = otherServiceAccount
		})

		Expect(waitReason(rolePodIdentity, req, v1.ConditionReady, pluginkit.ReasonReconcileError)).
			To(ContainSubstring("policy/missing"))
		Expect(associationsIn(rolePodIdentity, ns)).To(ConsistOf(HavePrefix(otherServiceAccount + "=")))
	})

	It("deletes the association of a former ServiceAccount after a refused inline policy", func() {
		ns := newNamespace(rolePodIdentity)
		req := connectorsRequest(rolePodIdentity, ns, managedParams(policyA))
		Expect(rolePodIdentity.k8s.Create(ctx, req)).To(Succeed())
		waitReady(rolePodIdentity, req)
		update(rolePodIdentity, req, func(r *pluginsv1alpha1.PluginRequest) {
			r.Spec.Parameters = raw(`{"managedPolicyArns":["` + policyA + `"],"inlinePolicy":{"Version":"2012-10-17",` +
				`"Statement":[{"Sid":"` + strings.Repeat("a", 10300) + `","Effect":"Allow","Action":"sqs:SendMessage",` +
				`"Resource":"*"}]}}`)
		})
		waitReason(rolePodIdentity, req, v1.ConditionReady, pluginsv1alpha1.ReasonInvalidParameters)

		update(rolePodIdentity, req, func(r *pluginsv1alpha1.PluginRequest) {
			r.Spec.Parameters = raw(
				`{"managedPolicyArns":["` + policyA + `","arn:aws:iam::111122223333:policy/missing"]}`,
			)
			r.Spec.Target.ServiceAccountName = otherServiceAccount
		})

		waitReason(rolePodIdentity, req, v1.ConditionReady, pluginkit.ReasonReconcileError)
		Eventually(func(g Gomega) {
			g.Expect(associationsIn(rolePodIdentity, ns)).NotTo(ContainElement(HavePrefix("orders-connectors=")))
		}, timeout).Should(Succeed())
	})

	It("attaches a policy again that was detached in AWS, after the resync", func() {
		ns := newNamespace(rolePodIdentity)
		req := connectorsRequest(rolePodIdentity, ns, managedParams(policyA))
		Expect(rolePodIdentity.k8s.Create(ctx, req)).To(Succeed())
		waitReady(rolePodIdentity, req)
		name := rolePodIdentity.roleName(ns)
		settledRoleCalls(rolePodIdentity, name)

		_, err := rolePodIdentity.iam.DetachRolePolicy(ctx, &iam.DetachRolePolicyInput{
			RoleName:  aws.String(name),
			PolicyArn: aws.String(policyA),
		})
		Expect(err).NotTo(HaveOccurred())

		Eventually(func(g Gomega) {
			iamRole, ok := rolePodIdentity.iam.Role(name)
			g.Expect(ok).To(BeTrue())
			g.Expect(iamRole.Attached).To(ContainElement(policyA))
		}, timeout).Should(Succeed())
	})

	It("refuses a role of its name that another request owns", func() {
		ns := newNamespace(role)
		name := role.roleName(ns)
		owner := awskit.ExtraTags(nil).
			OwnerTags(ns, "orders", "orders-connectors-permissions", "uid-of-an-orphaned-request")
		_, _, err := awskit.EnsureRole(ctx, role.iam, awskit.RoleSpec{
			Name:        name,
			TrustPolicy: []byte(`{"Version":"2012-10-17","Statement":[]}`),
			Tags:        owner,
		})
		Expect(err).NotTo(HaveOccurred())
		before := len(role.iam.RoleCalls(name))

		req := connectorsRequest(role, ns, paramsOf(role))
		Expect(role.k8s.Create(ctx, req)).To(Succeed())

		for _, conditionType := range []string{pluginsv1alpha1.ConditionOutputsReady, v1.ConditionReady} {
			message := waitReason(role, req, conditionType, awsconnectorsrole.ReasonRoleNameInUse)
			Expect(message).To(ContainSubstring("uid-of-an-orphaned-request"))
		}
		Expect(role.iam.RoleCalls(name)[before:]).To(HaveEach("GetRole"))
		got, _ := role.iam.Role(name)
		Expect(got.Tags).To(Equal(owner))
	})

	It("keeps the resourceVersion of a Ready request while nothing changes", func() {
		ns := newNamespace(role)
		req := connectorsRequest(role, ns, managedParams(policyA))
		Expect(role.k8s.Create(ctx, req)).To(Succeed())
		got := waitReady(role, req)

		Consistently(func(g Gomega) {
			g.Expect(read(g, role, req).ResourceVersion).To(Equal(got.ResourceVersion))
		}, 3*time.Second).Should(Succeed())

		var events eventsv1.EventList
		Expect(role.k8s.List(ctx, &events, client.InNamespace(ns))).To(Succeed())
		var created int
		for _, e := range events.Items {
			if e.Reason == "RoleCreated" {
				created++
			}
		}
		Expect(created).To(Equal(1))
	})
})
