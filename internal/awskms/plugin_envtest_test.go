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

package awskms_test

import (
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
	v1 "github.com/konsole-is/camunda-operator/api/v1"

	"github.com/konsole-is/camunda-operator-plugins/internal/awskms"
	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit/awsfake"
)

const timeout = 30 * time.Second

func read(g Gomega, request *pluginsv1alpha1.PluginRequest) *pluginsv1alpha1.PluginRequest {
	got := request.DeepCopy()
	g.Expect(mgr.GetAPIReader().Get(ctx, client.ObjectKeyFromObject(request), got)).To(Succeed())
	return got
}

func conditionOf(conditions []metav1.Condition, conditionType string) metav1.Condition {
	if c := meta.FindStatusCondition(conditions, conditionType); c != nil {
		return *c
	}
	return metav1.Condition{}
}

// aliasOf is the alias of the claim orders in namespace.
func aliasOf(namespace string) string {
	return "alias/camunda/" + namespace + "/orders"
}

// keyOf returns the key in region that the alias of the claim orders in
// namespace names in the home region.
func keyOf(g Gomega, namespace, region string) awsfake.Key {
	id, ok := fake.AliasTarget(home, aliasOf(namespace))
	g.Expect(ok).To(BeTrue(), "the alias of %s/orders exists", namespace)
	key, ok := fake.Key(region, id)
	g.Expect(ok).To(BeTrue(), "the key %s exists in %s", id, region)
	return key
}

// keysOf returns the keys in region whose namespace tag is namespace, or
// whose ID is the ID of such a key in the home region.
func keysOf(namespace, region string) []awsfake.Key {
	ids := map[string]bool{}
	for _, k := range fake.Keys() {
		if k.Region == home && k.Tags[awskit.TagNamespace] == namespace {
			ids[k.ID] = true
		}
	}

	var keys []awsfake.Key
	for _, k := range fake.Keys() {
		if k.Region == region && (ids[k.ID] || k.Tags[awskit.TagNamespace] == namespace) {
			keys = append(keys, k)
		}
	}
	return keys
}

// readyRequest creates request and waits until it is Ready.
func readyRequest(request *pluginsv1alpha1.PluginRequest) {
	GinkgoHelper()
	Expect(k8s.Create(ctx, request)).To(Succeed())
	Eventually(func(g Gomega) {
		c := conditionOf(read(g, request).Status.Conditions, v1.ConditionReady)
		g.Expect(c.Status).To(Equal(metav1.ConditionTrue), "%s: %s", c.Reason, c.Message)
	}, timeout).Should(Succeed())
}

// deleteRequest deletes request and waits until it is gone.
func deleteRequest(request *pluginsv1alpha1.PluginRequest) {
	GinkgoHelper()
	Expect(k8s.Delete(ctx, request)).To(Succeed())
	Eventually(func(g Gomega) {
		err := mgr.GetAPIReader().Get(ctx, client.ObjectKeyFromObject(request), request.DeepCopy())
		g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
	}, timeout).Should(Succeed())
}

// eventReasons returns the reasons of the events in namespace.
func eventReasons(g Gomega, namespace string) []string {
	var events corev1.EventList
	g.Expect(k8s.List(ctx, &events, client.InNamespace(namespace))).To(Succeed())
	reasons := make([]string, 0, len(events.Items))
	for _, e := range events.Items {
		reasons = append(reasons, e.Reason)
	}
	return reasons
}

var _ = Describe("the AWS KMS plugin", func() {
	It("serves a request with a rotating key and its alias", func() {
		ns := newNamespace()
		request := kmsRequest(singleRegion, ns, "", pluginsv1alpha1.DeletionPolicyDelete)
		readyRequest(request)

		Eventually(func(g Gomega) {
			key := keyOf(g, ns, home)
			g.Expect(key.Rotation).To(BeTrue())
			g.Expect(key.MultiRegion).To(BeFalse())
			g.Expect(key.Tags).To(HaveKeyWithValue("team", "platform"))
			g.Expect(key.Tags).To(HaveKeyWithValue(awskit.TagRequest, request.Name))

			got := read(g, request)
			g.Expect(conditionOf(got.Status.Conditions, pluginsv1alpha1.ConditionOutputsReady).Status).
				To(Equal(metav1.ConditionTrue))
			g.Expect(got.Status.Outputs).To(Equal(map[string]string{"keyRef": key.ARN, "format": "aws-kms-arn"}))
			g.Expect(got.Status.Links).To(HaveLen(1))
			g.Expect(eventReasons(g, ns)).To(ContainElement("KeyCreated"))
		}, timeout).Should(Succeed())
	})

	It("creates the replica of the multi-Region registration", func() {
		ns := newNamespace()
		request := kmsRequest(
			multiRegion,
			ns,
			`{"replicaRegions":["eu-central-1"]}`,
			pluginsv1alpha1.DeletionPolicyDelete,
		)
		readyRequest(request)

		Eventually(func(g Gomega) {
			primary := keyOf(g, ns, home)
			replicaKey := keyOf(g, ns, replica)
			g.Expect(primary.MultiRegion).To(BeTrue())
			g.Expect(replicaKey.State).To(Equal(types.KeyStateEnabled))
			g.Expect(replicaKey.Tags).To(Equal(primary.Tags))
			g.Expect(read(g, request).Status.Resources).To(HaveLen(2))
		}, timeout).Should(Succeed())
	})

	It("schedules the deletion with the window of the request when the policy is Delete", func() {
		ns := newNamespace()
		request := kmsRequest(singleRegion, ns, `{"pendingWindowDays":7}`, pluginsv1alpha1.DeletionPolicyDelete)
		readyRequest(request)
		var id string
		Eventually(func(g Gomega) { id = keyOf(g, ns, home).ID }, timeout).Should(Succeed())

		deleteRequest(request)

		key, _ := fake.Key(home, id)
		Expect(key.State).To(Equal(types.KeyStatePendingDeletion))
		Expect(key.PendingWindowDays).To(Equal(int32(7)))
		_, ok := fake.AliasTarget(home, aliasOf(ns))
		Expect(ok).To(BeFalse(), "the alias is gone")
	})

	It("schedules the deletion in 30 days when the request sets no window", func() {
		ns := newNamespace()
		request := kmsRequest(singleRegion, ns, "", pluginsv1alpha1.DeletionPolicyDelete)
		readyRequest(request)
		var id string
		Eventually(func(g Gomega) { id = keyOf(g, ns, home).ID }, timeout).Should(Succeed())

		deleteRequest(request)

		key, _ := fake.Key(home, id)
		Expect(key.State).To(Equal(types.KeyStatePendingDeletion))
		Expect(key.PendingWindowDays).To(Equal(int32(30)))
	})

	It("keeps the key enabled and its alias when the policy is Orphan", func() {
		ns := newNamespace()
		request := kmsRequest(singleRegion, ns, "", pluginsv1alpha1.DeletionPolicyOrphan)
		readyRequest(request)

		deleteRequest(request)

		Eventually(func(g Gomega) {
			key := keyOf(g, ns, home)
			g.Expect(key.State).To(Equal(types.KeyStateEnabled))
			g.Expect(key.Tags).NotTo(HaveKey(awskit.TagRequest))
			g.Expect(key.Tags).To(HaveKeyWithValue("team", "platform"))
		}, timeout).Should(Succeed())
	})

	It("leaves a key of the same request name in another cluster alone", func() {
		ns := newNamespace()
		request := kmsRequest(singleRegion, ns, "", pluginsv1alpha1.DeletionPolicyDelete)
		readyRequest(request)
		var key awsfake.Key
		Eventually(func(g Gomega) { key = keyOf(g, ns, home) }, timeout).Should(Succeed())
		// The key now looks like the key of a request of the same namespace
		// and name in another Kubernetes cluster.
		_, err := fake.TagResource(ctx, &kms.TagResourceInput{
			KeyId: aws.String(key.ID),
			Tags:  []types.Tag{{TagKey: aws.String("camunda.io/request-uid"), TagValue: aws.String("other-cluster")}},
		})
		Expect(err).NotTo(HaveOccurred())

		// An update of the request makes the plugin reconcile it again.
		Eventually(func(g Gomega) {
			got := read(g, request)
			got.Annotations = map[string]string{"test/poke": "1"}
			g.Expect(k8s.Update(ctx, got)).To(Succeed())
		}, timeout).Should(Succeed())
		Eventually(func(g Gomega) {
			c := conditionOf(read(g, request).Status.Conditions, pluginsv1alpha1.ConditionOutputsReady)
			g.Expect(c.Reason).To(Equal(awskms.ReasonKeyAliasInUse))
		}, timeout).Should(Succeed())

		deleteRequest(request)

		kept, _ := fake.Key(home, key.ID)
		Expect(kept.State).To(Equal(types.KeyStateEnabled))
		Expect(kept.Tags).To(HaveKeyWithValue(awskit.TagRequest, request.Name))
		target, _ := fake.AliasTarget(home, aliasOf(ns))
		Expect(target).To(Equal(key.ID), "the alias stays")
	})

	It("reports KeyAliasInUse for a new request of an orphaned claim", func() {
		ns := newNamespace()
		request := kmsRequest(singleRegion, ns, "", pluginsv1alpha1.DeletionPolicyOrphan)
		readyRequest(request)
		var id string
		Eventually(func(g Gomega) { id = keyOf(g, ns, home).ID }, timeout).Should(Succeed())
		deleteRequest(request)

		again := kmsRequest(singleRegion, ns, "", pluginsv1alpha1.DeletionPolicyDelete)
		Expect(k8s.Create(ctx, again)).To(Succeed())

		Eventually(func(g Gomega) {
			c := conditionOf(read(g, again).Status.Conditions, pluginsv1alpha1.ConditionOutputsReady)
			g.Expect(c.Status).To(Equal(metav1.ConditionFalse))
			g.Expect(c.Reason).To(Equal(awskms.ReasonKeyAliasInUse))
			g.Expect(c.Message).To(ContainSubstring(aliasOf(ns)))
		}, timeout).Should(Succeed())
		target, _ := fake.AliasTarget(home, aliasOf(ns))
		Expect(target).To(Equal(id), "the alias still names the kept key")
		Expect(keysOf(ns, home)).To(BeEmpty(), "no second key exists: a new key would carry the namespace tag")
	})
})
