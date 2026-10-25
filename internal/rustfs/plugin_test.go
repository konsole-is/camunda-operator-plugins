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

package rustfs

import (
	"bytes"
	"errors"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
	v1 "github.com/konsole-is/camunda-operator/api/v1"

	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

// gone reports whether obj does not exist.
func gone(g Gomega, obj client.Object) bool {
	err := mgr.GetAPIReader().Get(ctx, client.ObjectKeyFromObject(obj), obj)
	g.Expect(client.IgnoreNotFound(err)).To(Succeed())
	return apierrors.IsNotFound(err)
}

func secretOf(request *pluginsv1alpha1.PluginRequest) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: request.Namespace, Name: secretName(request.Name)}}
}

func contractOf(request *pluginsv1alpha1.PluginRequest) *v1.ObjectStorageConfig {
	return &v1.ObjectStorageConfig{ObjectMeta: metav1.ObjectMeta{Namespace: request.Namespace, Name: request.Name}}
}

// touch changes an annotation of request, which starts a reconcile.
func touch(request *pluginsv1alpha1.PluginRequest) {
	GinkgoHelper()
	Eventually(func(g Gomega) {
		read := readRequest(g, request)
		read.Annotations = map[string]string{"example.com/touch": time.Now().String()}
		g.Expect(k8s.Update(ctx, read)).To(Succeed())
	}, timeout).Should(Succeed())
}

// settle waits until request stays unchanged for 3 seconds, so that no
// reconcile that read it before a change of the spec is still running.
func settle(request *pluginsv1alpha1.PluginRequest) {
	GinkgoHelper()
	Eventually(func(g Gomega) {
		before := readRequest(g, request).ResourceVersion
		time.Sleep(3 * time.Second)
		g.Expect(readRequest(g, request).ResourceVersion).To(Equal(before))
	}, timeout).Should(Succeed())
}

// driftEvents returns the DriftCorrected events on request.
func driftEvents(g Gomega, request *pluginsv1alpha1.PluginRequest) []eventsv1.Event {
	read := readRequest(g, request)
	var list eventsv1.EventList
	g.Expect(k8s.List(ctx, &list, client.InNamespace(request.Namespace))).To(Succeed())

	var found []eventsv1.Event
	for _, e := range list.Items {
		if e.Regarding.UID == read.UID && e.Reason == "DriftCorrected" {
			found = append(found, e)
		}
	}
	return found
}

var _ = Describe("the rustfs plugin", func() {
	It("creates a bucket, a user that only that bucket accepts, a Secret, and the contract", func() {
		ns := newNamespace()
		request := bucketRequest(ns, pluginsv1alpha1.DeletionPolicyDelete)
		readyRequest(request)
		name := resourceName(ns, request.Name)

		read := readRequest(Default, request)
		Expect(condition(read.Status.Conditions, pluginsv1alpha1.ConditionOutputsReady).Status).
			To(Equal(metav1.ConditionTrue))
		Expect(read.Status.Outputs).To(Equal(map[string]string{outputObjectStorageConfig: request.Name}))

		Expect(fake.hasBucket(name)).To(BeTrue())
		user, ok := fake.user(name)
		Expect(ok).To(BeTrue())
		Expect(user.policy).To(Equal(name))
		policy, ok := fake.policy(name)
		Expect(ok).To(BeTrue())
		Expect(policy).To(Equal(userPolicy(name)))

		secret := readSecret(Default, request)
		Expect(string(secret.Data[secretKeyAccessKeyID])).To(Equal(name))
		Expect(secret.Data[secretKeySecretAccessKey]).To(HaveLen(40))
		Expect(string(secret.Data[secretKeySecretAccessKey])).To(Equal(user.secretKey))

		contract := contractOf(request)
		Expect(mgr.GetAPIReader().Get(ctx, client.ObjectKeyFromObject(contract), contract)).To(Succeed())
		Expect(contract.Spec.Type).To(Equal(v1.ObjectStorageTypeS3))
		Expect(contract.Spec.S3).To(Equal(&v1.S3Storage{
			BucketName:     name,
			Region:         v1.PlaceholderS3Region,
			Endpoint:       suiteSettings.Endpoint,
			ForcePathStyle: true,
			Auth: v1.S3StorageAuth{
				Type: v1.ObjectStorageAuthTypeCredentials,
				Credentials: &v1.S3Credentials{SecretRef: v1.S3CredentialsSecretRef{
					Name:               secretName(request.Name),
					AccessKeyIDKey:     secretKeyAccessKeyID,
					SecretAccessKeyKey: secretKeySecretAccessKey,
				}},
			},
		}))

		Expect(read.Status.Summary[0]).To(And(HaveField("Label", "Bucket"), HaveField("Value", name)))
		Expect(read.Status.Links).To(Equal(links(suiteSettings.ConsoleURL)))
		Expect(read.Status.Resources).To(Equal(resources(name, suiteSettings.Endpoint, request.Name)))
	})

	It("keeps the key on a later reconcile", func() {
		ns := newNamespace()
		request := bucketRequest(ns, pluginsv1alpha1.DeletionPolicyDelete)
		readyRequest(request)
		calls := fake.addUsers()
		key := readSecret(Default, request).Data[secretKeySecretAccessKey]

		Eventually(func(g Gomega) {
			read := readRequest(g, request)
			read.Annotations = map[string]string{"example.com/touch": "1"}
			g.Expect(k8s.Update(ctx, read)).To(Succeed())
		}, timeout).Should(Succeed())

		Consistently(func(g Gomega) {
			g.Expect(fake.addUsers()).To(Equal(calls))
			g.Expect(readSecret(g, request).Data[secretKeySecretAccessKey]).To(Equal(key))
		}, 3*time.Second).Should(Succeed())
	})

	It("issues a new key when the Secret is gone", func() {
		ns := newNamespace()
		request := bucketRequest(ns, pluginsv1alpha1.DeletionPolicyDelete)
		readyRequest(request)
		calls := fake.addUsers()
		oldKey := readSecret(Default, request).Data[secretKeySecretAccessKey]

		// A reconcile that read the Secret before the delete writes it again
		// with the old key, so the spec deletes it until a new key shows.
		Eventually(func(g Gomega) {
			secret := readSecret(g, request)
			if bytes.Equal(secret.Data[secretKeySecretAccessKey], oldKey) {
				g.Expect(client.IgnoreNotFound(k8s.Delete(ctx, secret))).To(Succeed())
			}
			g.Expect(secret.Data[secretKeySecretAccessKey]).NotTo(Equal(oldKey))
			user, _ := fake.user(resourceName(ns, request.Name))
			g.Expect(string(secret.Data[secretKeySecretAccessKey])).To(Equal(user.secretKey))
			g.Expect(fake.addUsers()).To(BeNumerically(">", calls))
		}, timeout).Should(Succeed())
	})

	It("creates again what was deleted in RustFS, and reports the drift", func() {
		ns := newNamespace()
		request := bucketRequest(ns, pluginsv1alpha1.DeletionPolicyDelete)
		readyRequest(request)
		settle(request)
		name := resourceName(ns, request.Name)
		Expect(driftEvents(Default, request)).To(BeEmpty(), "the first create is not drift")

		_, err := fake.DeleteBucket(ctx, name)
		Expect(err).NotTo(HaveOccurred())
		Expect(fake.RemovePolicy(ctx, name)).To(Succeed())
		Expect(fake.RemoveUser(ctx, name)).To(Succeed())
		touch(request)

		Eventually(func(g Gomega) {
			g.Expect(fake.hasBucket(name)).To(BeTrue())
			_, ok := fake.policy(name)
			g.Expect(ok).To(BeTrue())
			user, ok := fake.user(name)
			g.Expect(ok).To(BeTrue())
			g.Expect(user.policy).To(Equal(name))

			events := driftEvents(g, request)
			g.Expect(events).To(HaveLen(1))
			g.Expect(events[0].Note).To(SatisfyAll(
				ContainSubstring("created the bucket "+name+", which was gone"),
				ContainSubstring("created the policy "+name+", which was gone"),
				ContainSubstring("created the user "+name+", which was gone"),
			))
			g.Expect(readRequest(g, request).Status.Summary).
				To(ContainElement(HaveField("Label", "Last drift corrected")))
		}, timeout).Should(Succeed())
	})

	It("reports a bucket that it created again when a later step fails", func() {
		ns := newNamespace()
		request := bucketRequest(ns, pluginsv1alpha1.DeletionPolicyDelete)
		readyRequest(request)
		settle(request)
		name := resourceName(ns, request.Name)

		_, err := fake.DeleteBucket(ctx, name)
		Expect(err).NotTo(HaveOccurred())
		Expect(fake.RemoveUser(ctx, name)).To(Succeed())
		fake.fail("AddUser", errors.New("RustFS refused the user"))
		DeferCleanup(func() { fake.heal("AddUser") })
		touch(request)

		Eventually(func(g Gomega) {
			read := readRequest(g, request)
			g.Expect(condition(read.Status.Conditions, v1.ConditionReady).Reason).
				To(Equal(pluginkit.ReasonReconcileError))
			events := driftEvents(g, request)
			g.Expect(events).To(HaveLen(1))
			g.Expect(events[0].Note).To(ContainSubstring("created the bucket " + name + ", which was gone"))
			g.Expect(read.Status.Summary).To(ContainElement(HaveField("Label", "Last drift corrected")))
		}, timeout).Should(Succeed())
	})

	It("attaches the policy again when it was detached in RustFS, and reports the drift", func() {
		ns := newNamespace()
		request := bucketRequest(ns, pluginsv1alpha1.DeletionPolicyDelete)
		readyRequest(request)
		settle(request)
		name := resourceName(ns, request.Name)

		Expect(fake.AttachPolicy(ctx, "", name)).To(Succeed())
		touch(request)

		Eventually(func(g Gomega) {
			user, _ := fake.user(name)
			g.Expect(user.policy).To(Equal(name))
			events := driftEvents(g, request)
			g.Expect(events).To(HaveLen(1))
			g.Expect(events[0].Note).
				To(ContainSubstring("attached the policy " + name + " to the user " + name + " again"))
		}, timeout).Should(Succeed())
	})

	It("reports a refused admin call", func() {
		fake.fail(
			"AddUser",
			&AdminError{Call: "add-user", Status: 403, Code: "AccessDenied", Message: "Access Denied."},
		)
		DeferCleanup(fake.heal, "AddUser")

		ns := newNamespace()
		request := bucketRequest(ns, pluginsv1alpha1.DeletionPolicyDelete)
		Expect(k8s.Create(ctx, request)).To(Succeed())

		Eventually(func(g Gomega) {
			ready := condition(readRequest(g, request).Status.Conditions, v1.ConditionReady)
			g.Expect(ready.Status).To(Equal(metav1.ConditionFalse))
			g.Expect(ready.Reason).To(Equal(pluginkit.ReasonReconcileError))
			g.Expect(ready.Message).To(ContainSubstring("RustFS refused add-user: AccessDenied"))
		}, timeout).Should(Succeed())

		fake.heal("AddUser")
		Eventually(func(g Gomega) {
			ready := condition(readRequest(g, request).Status.Conditions, v1.ConditionReady)
			g.Expect(ready.Status).To(Equal(metav1.ConditionTrue))
		}, timeout).Should(Succeed())
	})

	It("deletes the bucket, the user, the policy, the Secret, and the contract with Delete", func() {
		ns := newNamespace()
		request := bucketRequest(ns, pluginsv1alpha1.DeletionPolicyDelete)
		readyRequest(request)
		name := resourceName(ns, request.Name)

		Expect(k8s.Delete(ctx, request)).To(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(gone(g, &pluginsv1alpha1.PluginRequest{ObjectMeta: request.ObjectMeta})).To(BeTrue())
		}, timeout).Should(Succeed())

		Expect(fake.hasBucket(name)).To(BeFalse())
		_, ok := fake.user(name)
		Expect(ok).To(BeFalse())
		_, ok = fake.policy(name)
		Expect(ok).To(BeFalse())
		Expect(gone(Default, secretOf(request))).To(BeTrue())
		Expect(gone(Default, contractOf(request))).To(BeTrue())
	})

	It("keeps everything with Orphan", func() {
		ns := newNamespace()
		request := bucketRequest(ns, pluginsv1alpha1.DeletionPolicyOrphan)
		readyRequest(request)
		name := resourceName(ns, request.Name)

		Expect(k8s.Delete(ctx, request)).To(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(gone(g, &pluginsv1alpha1.PluginRequest{ObjectMeta: request.ObjectMeta})).To(BeTrue())
		}, timeout).Should(Succeed())

		Expect(fake.hasBucket(name)).To(BeTrue())
		_, ok := fake.user(name)
		Expect(ok).To(BeTrue())
		_, ok = fake.policy(name)
		Expect(ok).To(BeTrue())
		for _, obj := range []client.Object{secretOf(request), contractOf(request)} {
			Expect(mgr.GetAPIReader().Get(ctx, client.ObjectKeyFromObject(obj), obj)).To(Succeed())
			Expect(obj.GetLabels()).To(HaveKeyWithValue(pluginsv1alpha1.LabelRegistration, registration))
			Expect(obj.GetLabels()).To(HaveKeyWithValue(pluginsv1alpha1.LabelRequest, request.Name))
			Expect(obj.GetOwnerReferences()).To(BeEmpty())
		}
	})

	It("keeps the finalizer while the bucket cannot be deleted", func() {
		ns := newNamespace()
		request := bucketRequest(ns, pluginsv1alpha1.DeletionPolicyDelete)
		readyRequest(request)

		fake.fail("DeleteBucket", errors.New("bucket busy"))
		DeferCleanup(fake.heal, "DeleteBucket")
		Expect(k8s.Delete(ctx, request)).To(Succeed())

		Eventually(func(g Gomega) {
			ready := condition(readRequest(g, request).Status.Conditions, v1.ConditionReady)
			g.Expect(ready.Status).To(Equal(metav1.ConditionFalse))
			g.Expect(ready.Reason).To(Equal(pluginkit.ReasonDeleteFailed))
			g.Expect(ready.Message).To(ContainSubstring("bucket busy"))
		}, timeout).Should(Succeed())

		fake.heal("DeleteBucket")
		Eventually(func(g Gomega) {
			g.Expect(gone(g, &pluginsv1alpha1.PluginRequest{ObjectMeta: request.ObjectMeta})).To(BeTrue())
		}, timeout).Should(Succeed())
	})

	Context("while RustFS does not answer", Serial, func() {
		It("marks the registration Unhealthy", func() {
			fake.fail("Ping", errors.New("dial tcp: connection refused"))
			DeferCleanup(fake.heal, "Ping")

			reg := &pluginsv1alpha1.PluginRegistration{ObjectMeta: metav1.ObjectMeta{Name: registration}}
			Eventually(func(g Gomega) {
				g.Expect(k8s.Get(ctx, client.ObjectKeyFromObject(reg), reg)).To(Succeed())
				ready := condition(reg.Status.Conditions, v1.ConditionReady)
				g.Expect(ready.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(ready.Reason).To(Equal(pluginkit.ReasonUnhealthy))
				g.Expect(ready.Message).To(ContainSubstring("connection refused"))
			}, timeout).Should(Succeed())

			fake.heal("Ping")
			Eventually(func(g Gomega) {
				g.Expect(k8s.Get(ctx, client.ObjectKeyFromObject(reg), reg)).To(Succeed())
				g.Expect(condition(reg.Status.Conditions, v1.ConditionReady).Status).To(Equal(metav1.ConditionTrue))
			}, timeout).Should(Succeed())
		})
	})
})
