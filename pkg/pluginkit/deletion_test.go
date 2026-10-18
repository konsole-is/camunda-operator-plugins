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

package pluginkit_test

import (
	"errors"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"

	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

func gone(obj client.Object) func() bool {
	return func() bool {
		return apierrors.IsNotFound(k8s.Get(ctx, client.ObjectKeyFromObject(obj), obj))
	}
}

func grantsOf(g Gomega, request *pluginsv1alpha1.PluginRequest) []pluginsv1alpha1.Grant {
	var list pluginsv1alpha1.GrantList
	g.Expect(k8s.List(ctx, &list, client.InNamespace(request.Namespace),
		client.MatchingLabels{pluginsv1alpha1.LabelRequest: request.Name})).To(Succeed())
	return list.Items
}

// servedWithKey creates a request with the key input, so that it has both
// grants, and waits until it is Ready.
func servedWithKey(ns string, mutate ...func(*pluginsv1alpha1.PluginRequest)) *pluginsv1alpha1.PluginRequest {
	GinkgoHelper()
	request := createRequest(ns, append([]func(*pluginsv1alpha1.PluginRequest){
		func(r *pluginsv1alpha1.PluginRequest) { r.Spec.Inputs = map[string]string{"key": "k"} },
	}, mutate...)...)
	waitReady(request)
	Eventually(func(g Gomega) { g.Expect(grantsOf(g, request)).To(HaveLen(2)) }).Should(Succeed())
	return request
}

var _ = Describe("the deletion of a request", func() {
	It("keeps the finalizer until the delete of the plugin succeeds", func() {
		request := servedWithKey("delete-fails")
		plugin.setDeleteErr(errors.New("the bucket is not empty"))
		DeferCleanup(func() { plugin.setDeleteErr(nil) })

		Expect(k8s.Delete(ctx, request)).To(Succeed())

		Eventually(func(g Gomega) {
			g.Expect(condition(g, request, "Ready").Reason).To(Equal(pluginkit.ReasonDeleteFailed))
		}).Should(Succeed())
		Consistently(func(g Gomega) {
			g.Expect(readRequest(g, request).Finalizers).To(ContainElement(pluginsv1alpha1.FinalizerCleanup))
		}).WithTimeout(2 * time.Second).Should(Succeed())

		plugin.setDeleteErr(nil)
		Eventually(gone(request)).Should(BeTrue())
	})

	It("deletes the resources of a request with the Delete policy", func() {
		request := servedWithKey("delete-policy")
		sc := &storagev1.StorageClass{}
		sc.Name = storageClassName(request)

		Expect(k8s.Delete(ctx, request)).To(Succeed())

		Eventually(gone(request)).Should(BeTrue())
		Expect(gone(sc)()).To(BeTrue(), "the plugin deletes its cluster-scoped object")
		Expect(grantsOf(Default, request)).To(BeEmpty())

		// envtest runs no garbage collector, which deletes the ConfigMap in a
		// real cluster.
		var cm corev1.ConfigMap
		Expect(k8s.Get(ctx, client.ObjectKey{Namespace: request.Namespace, Name: testRequestName + "-data"}, &cm)).
			To(Succeed())
		Expect(cm.OwnerReferences).To(ConsistOf(HaveField("Kind", "PluginRequest")))
	})

	It("deletes a grant whose owner reference was removed", func() {
		request := servedWithKey("grant-without-owner")
		plugin.setDeleteErr(errors.New("not yet"))
		DeferCleanup(func() { plugin.setDeleteErr(nil) })

		// The failing delete holds the request, so that no reconcile applies
		// the Grant again with its owner reference.
		Expect(k8s.Delete(ctx, request)).To(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(condition(g, request, "Ready").Reason).To(Equal(pluginkit.ReasonDeleteFailed))
		}).Should(Succeed())
		Eventually(func(g Gomega) {
			grant := readGrant(g, request)
			grant.OwnerReferences = nil
			g.Expect(k8s.Update(ctx, grant)).To(Succeed())
		}).Should(Succeed())

		plugin.setDeleteErr(nil)

		Eventually(gone(request)).Should(BeTrue())
		Expect(grantsOf(Default, request)).To(BeEmpty())
	})

	It("waits for a grant that holds its own finalizer", func() {
		request := servedWithKey("grant-finalizer")
		Eventually(func(g Gomega) {
			grant := readGrant(g, request)
			controllerutil.AddFinalizer(grant, "example.com/hold")
			g.Expect(k8s.Update(ctx, grant)).To(Succeed())
		}).Should(Succeed())

		Expect(k8s.Delete(ctx, request)).To(Succeed())

		Consistently(func(g Gomega) {
			g.Expect(readRequest(g, request).Finalizers).To(ContainElement(pluginsv1alpha1.FinalizerCleanup))
		}).WithTimeout(2 * time.Second).Should(Succeed())

		Eventually(func(g Gomega) {
			grant := readGrant(g, request)
			controllerutil.RemoveFinalizer(grant, "example.com/hold")
			g.Expect(k8s.Update(ctx, grant)).To(Succeed())
		}).Should(Succeed())
		Eventually(gone(request)).Should(BeTrue())
	})

	It("keeps the resources of a request with the Orphan policy", func() {
		request := servedWithKey("orphan-policy", func(r *pluginsv1alpha1.PluginRequest) {
			r.Spec.DeletionPolicy = pluginsv1alpha1.DeletionPolicyOrphan
		})
		uid := readRequest(Default, request).UID
		wantLabels := HaveKeyWithValue(pluginsv1alpha1.LabelRequest, testRequestName)

		Expect(k8s.Delete(ctx, request)).To(Succeed())

		Eventually(gone(request)).Should(BeTrue())
		var cm corev1.ConfigMap
		Expect(k8s.Get(ctx, client.ObjectKey{Namespace: request.Namespace, Name: testRequestName + "-data"}, &cm)).
			To(Succeed())
		Expect(cm.Labels).To(wantLabels)
		Expect(cm.OwnerReferences).NotTo(ContainElement(HaveField("UID", uid)))

		grants := grantsOf(Default, request)
		Expect(grants).To(HaveLen(2))
		for _, g := range grants {
			Expect(g.Labels).To(wantLabels)
			Expect(g.OwnerReferences).NotTo(ContainElement(HaveField("UID", uid)))
		}

		sc := &storagev1.StorageClass{}
		sc.Name = storageClassName(request)
		Expect(gone(sc)()).To(BeFalse())
	})

	It("calls Delete for a request whose parameters fail the schema", func() {
		request := servedWithKey("delete-invalid-parameters")
		updateRequest(request, func(r *pluginsv1alpha1.PluginRequest) {
			r.Spec.Parameters = &apiextensionsv1.JSON{Raw: []byte(`{"size":0}`)}
		})
		Eventually(func(g Gomega) {
			g.Expect(condition(g, request, "Ready").Reason).To(Equal(pluginsv1alpha1.ReasonInvalidParameters))
		}).Should(Succeed())

		Expect(k8s.Delete(ctx, request)).To(Succeed())

		sc := &storagev1.StorageClass{}
		sc.Name = storageClassName(request)
		Eventually(gone(request)).Should(BeTrue())
		Expect(gone(sc)()).To(BeTrue())
	})
})
