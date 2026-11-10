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

package awsebs_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
	v1 "github.com/konsole-is/camunda-operator/api/v1"

	"github.com/konsole-is/camunda-operator-plugins/internal/awsebs"
	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

var _ = Describe("the AWS EBS plugin", func() {
	It("publishes the registration with the required key input", func() {
		Eventually(func(g Gomega) {
			var reg pluginsv1alpha1.PluginRegistration
			g.Expect(k8s.Get(ctx, client.ObjectKey{Name: registration}, &reg)).To(Succeed())
			g.Expect(reg.Spec.Inputs).To(ConsistOf(pluginsv1alpha1.InputDecl{
				Name: "key", From: "encryption-key/keyRef", Required: true, Formats: []string{"aws-kms-arn"},
			}))
			g.Expect(reg.Spec.Outputs).To(ContainElement(HaveField("Key", "storageClassName")))
		}).Should(Succeed())
	})

	It("reports MissingInput and creates no StorageClass for a request without a key", func() {
		request := ebsRequest(newNamespace(), "", "", pluginsv1alpha1.DeletionPolicyDelete)
		Expect(k8s.Create(ctx, request)).To(Succeed())

		Eventually(func(g Gomega) {
			cond := condition(g, request, v1.ConditionReady)
			g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			g.Expect(cond.Reason).To(Equal(pluginkit.ReasonMissingInput))
			g.Expect(cond.Message).To(ContainSubstring("key (from encryption-key/keyRef)"))
		}).Should(Succeed())
		Consistently(storageClassExists(request)).WithTimeout(3 * time.Second).Should(BeFalse())

		updateRequest(request, func(r *pluginsv1alpha1.PluginRequest) {
			r.Spec.Inputs = map[string]string{"key": testKey}
		})
		Eventually(func(g Gomega) {
			g.Expect(liveStorageClass(g, request).Parameters).To(HaveKeyWithValue("kmsKeyId", testKey))
		}).Should(Succeed())
	})

	It("serves a request with a StorageClass that encrypts with the key", func() {
		request := ebsRequest(newNamespace(), testKey, `{"type":"gp3","throughput":250}`,
			pluginsv1alpha1.DeletionPolicyDelete)
		Expect(k8s.Create(ctx, request)).To(Succeed())
		name := awsebs.StorageClassName(request.Namespace, claimName, requestName)

		waitReady(request)
		sc := liveStorageClass(Default, request)
		Expect(sc.Name).To(Equal(name))
		Expect(sc.Provisioner).To(Equal("ebs.csi.aws.com"))
		Expect(sc.Parameters).To(Equal(map[string]string{
			"type":                      "gp3",
			"throughput":                "250",
			"csi.storage.k8s.io/fstype": "ext4",
			"encrypted":                 "true",
			"kmsKeyId":                  testKey,
		}))
		Expect(sc.AllowVolumeExpansion).To(Equal(new(true)))
		Expect(sc.VolumeBindingMode).To(Equal(ptr.To(storagev1.VolumeBindingWaitForFirstConsumer)))
		Expect(sc.ReclaimPolicy).To(Equal(ptr.To(corev1.PersistentVolumeReclaimDelete)))
		for k, v := range pluginkit.ClusterScopedLabels(request) {
			Expect(sc.Labels).To(HaveKeyWithValue(k, v))
		}
		Expect(sc.OwnerReferences).To(BeEmpty())

		console, err := awskit.KeyConsoleURL(testKey)
		Expect(err).NotTo(HaveOccurred())
		status := readRequest(Default, request).Status
		Expect(status.Outputs).To(Equal(map[string]string{"storageClassName": name}))
		Expect(status.Summary).To(Equal([]pluginsv1alpha1.SummaryItem{
			{Label: "StorageClass", Value: name, Severity: pluginsv1alpha1.SeverityInfo},
			{Label: "Volume type", Value: "gp3", Severity: pluginsv1alpha1.SeverityInfo},
			{Label: "Key", Value: testKey, Severity: pluginsv1alpha1.SeverityInfo},
		}))
		Expect(status.Links).To(Equal([]pluginsv1alpha1.Link{{Name: "KMS key", URL: console}}))
		Expect(status.Resources).To(Equal([]pluginsv1alpha1.ResourceRef{{
			Kind:     "StorageClass",
			Name:     name,
			Role:     "Volumes of the cluster",
			OnOrphan: pluginsv1alpha1.OnOrphanKept,
		}}))
		Expect(eventsOf(Default, request, "StorageClassCreated")).To(HaveLen(1))
	})

	It("replaces the StorageClass under the same name with one event when a parameter changes", func() {
		request := ebsRequest(newNamespace(), testKey, `{}`, pluginsv1alpha1.DeletionPolicyDelete)
		Expect(k8s.Create(ctx, request)).To(Succeed())
		waitReady(request)
		uid := liveStorageClass(Default, request).UID

		updateRequest(request, func(r *pluginsv1alpha1.PluginRequest) {
			r.Spec.Parameters = raw(`{"type":"io2","iops":6000}`)
		})

		Eventually(func(g Gomega) {
			sc := liveStorageClass(g, request)
			g.Expect(sc.UID).NotTo(Equal(uid))
			g.Expect(sc.Parameters).To(HaveKeyWithValue("type", "io2"))
		}).Should(Succeed())
		Consistently(func(g Gomega) {
			replaced := eventsOf(g, request, "StorageClassReplaced")
			g.Expect(replaced).To(HaveLen(1))
			g.Expect(replaced[0].Note).To(ContainSubstring("parameters.type"))
		}).WithTimeout(3 * resync).Should(Succeed())
		waitReady(request)
	})

	It("brings back a StorageClass that someone deleted", func() {
		request := ebsRequest(newNamespace(), testKey, "", pluginsv1alpha1.DeletionPolicyDelete)
		Expect(k8s.Create(ctx, request)).To(Succeed())
		waitReady(request)
		sc := liveStorageClass(Default, request)

		Expect(k8s.Delete(ctx, sc)).To(Succeed())

		Eventually(func(g Gomega) {
			again := liveStorageClass(g, request)
			g.Expect(again.UID).NotTo(Equal(sc.UID))
			g.Expect(again.Parameters).To(HaveKeyWithValue("kmsKeyId", testKey))
		}).Should(Succeed())
	})

	It("reports InvalidKey for a key that is not a KMS key ARN", func() {
		request := ebsRequest(newNamespace(), "not-an-arn", "", pluginsv1alpha1.DeletionPolicyDelete)
		Expect(k8s.Create(ctx, request)).To(Succeed())

		Eventually(func(g Gomega) {
			cond := condition(g, request, pluginsv1alpha1.ConditionOutputsReady)
			g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			g.Expect(cond.Reason).To(Equal(awsebs.ReasonInvalidKey))
			g.Expect(cond.Message).To(ContainSubstring("not-an-arn"))
		}).Should(Succeed())
		Consistently(storageClassExists(request)).WithTimeout(3 * time.Second).Should(BeFalse())
	})

	It("leaves a StorageClass of its name that it does not own alone", func() {
		request := ebsRequest(newNamespace(), testKey, "", pluginsv1alpha1.DeletionPolicyDelete)
		taken := &storagev1.StorageClass{
			ObjectMeta:  metav1.ObjectMeta{Name: storageClassNameOf(request)},
			Provisioner: "example.com/other",
		}
		Expect(k8s.Create(ctx, taken)).To(Succeed())
		Expect(k8s.Create(ctx, request)).To(Succeed())

		Eventually(func(g Gomega) {
			cond := condition(g, request, pluginsv1alpha1.ConditionOutputsReady)
			g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			g.Expect(cond.Reason).To(Equal(awsebs.ReasonStorageClassNameTaken))
		}).Should(Succeed())
		Consistently(func(g Gomega) {
			sc := liveStorageClass(g, request)
			g.Expect(sc.UID).To(Equal(taken.UID))
			g.Expect(sc.Provisioner).To(Equal("example.com/other"))
			g.Expect(sc.Labels).NotTo(HaveKey(pluginsv1alpha1.LabelRequest))
		}).WithTimeout(3 * resync).Should(Succeed())
	})

	It("deletes the StorageClass with the policy Delete", func() {
		request := ebsRequest(newNamespace(), testKey, "", pluginsv1alpha1.DeletionPolicyDelete)
		Expect(k8s.Create(ctx, request)).To(Succeed())
		waitReady(request)

		Expect(k8s.Delete(ctx, request)).To(Succeed())

		Eventually(gone(request)).Should(BeTrue())
		Expect(storageClassExists(request)()).To(BeFalse())
		Eventually(func(g Gomega) {
			deleted := eventsOf(g, request, "StorageClassDeleted")
			g.Expect(deleted).To(HaveLen(1))
			g.Expect(deleted[0].Note).To(ContainSubstring(storageClassNameOf(request)))
		}).Should(Succeed())
	})

	It("keeps the StorageClass and its labels with the policy Orphan", func() {
		request := ebsRequest(newNamespace(), testKey, "", pluginsv1alpha1.DeletionPolicyOrphan)
		Expect(k8s.Create(ctx, request)).To(Succeed())
		waitReady(request)

		Expect(k8s.Delete(ctx, request)).To(Succeed())

		Eventually(gone(request)).Should(BeTrue())
		sc := liveStorageClass(Default, request)
		for k, v := range pluginkit.ClusterScopedLabels(request) {
			Expect(sc.Labels).To(HaveKeyWithValue(k, v))
		}
	})
})

// waitReady waits until Ready of request is True.
func waitReady(request *pluginsv1alpha1.PluginRequest) {
	GinkgoHelper()
	Eventually(func(g Gomega) {
		cond := condition(g, request, v1.ConditionReady)
		g.Expect(cond.Status).To(Equal(metav1.ConditionTrue), "%s: %s", cond.Reason, cond.Message)
	}).Should(Succeed())
}

// gone reports whether request no longer exists.
func gone(request *pluginsv1alpha1.PluginRequest) func() bool {
	return func() bool {
		var current pluginsv1alpha1.PluginRequest
		return apierrors.IsNotFound(k8s.Get(ctx, client.ObjectKeyFromObject(request), &current))
	}
}
