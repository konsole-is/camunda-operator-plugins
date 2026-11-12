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

package awss3_test

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
	v1 "github.com/konsole-is/camunda-operator/api/v1"

	"github.com/konsole-is/camunda-operator-plugins/internal/awss3"
	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit/awsfake"
	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

func read(g Gomega, request *pluginsv1alpha1.PluginRequest) *pluginsv1alpha1.PluginRequest {
	got := request.DeepCopy()
	g.Expect(mgr.GetAPIReader().Get(ctx, client.ObjectKeyFromObject(request), got)).To(Succeed())
	return got
}

func conditionOf(g Gomega, request *pluginsv1alpha1.PluginRequest, conditionType string) metav1.Condition {
	if c := meta.FindStatusCondition(read(g, request).Status.Conditions, conditionType); c != nil {
		return *c
	}
	return metav1.Condition{}
}

// bucketNameOf is the bucket name of the claim orders in namespace with the
// default prefix.
func bucketNameOf(namespace string) string {
	return awss3.BucketName("camunda", namespace, "orders")
}

func bucketOf(g Gomega, name string) awsfake.Bucket {
	bucket, ok := fake.Bucket(name)
	g.Expect(ok).To(BeTrue(), "the bucket %s exists", name)
	return bucket
}

// bucketsOf returns the buckets whose namespace tag is namespace.
func bucketsOf(namespace string) []awsfake.Bucket {
	var out []awsfake.Bucket
	for _, b := range fake.Buckets() {
		if b.Tags[awskit.TagNamespace] == namespace {
			out = append(out, b)
		}
	}
	return out
}

func grantsOf(g Gomega, request *pluginsv1alpha1.PluginRequest) []pluginsv1alpha1.Grant {
	var list pluginsv1alpha1.GrantList
	g.Expect(k8s.List(ctx, &list, client.InNamespace(request.Namespace),
		client.MatchingLabels{pluginsv1alpha1.LabelRequest: request.Name})).To(Succeed())
	return list.Items
}

// readyGrants sets Ready=True on each Grant of request that has no Ready, as
// the identity extension would.
func readyGrants(g Gomega, request *pluginsv1alpha1.PluginRequest) {
	for _, grant := range grantsOf(g, request) {
		if meta.FindStatusCondition(grant.Status.Conditions, v1.ConditionReady) != nil {
			continue
		}
		meta.SetStatusCondition(&grant.Status.Conditions, metav1.Condition{
			Type:    v1.ConditionReady,
			Status:  metav1.ConditionTrue,
			Reason:  v1.ReasonHealthy,
			Message: "Set by the spec, which plays the identity extension",
		})
		g.Expect(k8s.Status().Update(ctx, &grant)).To(Succeed())
	}
}

// readyRequest creates request and waits until it is Ready, with its Grants
// Ready.
func readyRequest(request *pluginsv1alpha1.PluginRequest) {
	GinkgoHelper()
	Expect(k8s.Create(ctx, request)).To(Succeed())
	Eventually(func(g Gomega) {
		readyGrants(g, request)
		c := conditionOf(g, request, v1.ConditionReady)
		g.Expect(c.Status).To(Equal(metav1.ConditionTrue), "%s: %s", c.Reason, c.Message)
	}, timeout).Should(Succeed())
}

func gone(request *pluginsv1alpha1.PluginRequest) func() bool {
	return func() bool {
		err := mgr.GetAPIReader().Get(ctx, client.ObjectKeyFromObject(request), request.DeepCopy())
		return apierrors.IsNotFound(err)
	}
}

// poke changes an annotation of request, so that the plugin reconciles it
// again.
func poke(request *pluginsv1alpha1.PluginRequest) {
	GinkgoHelper()
	Eventually(func(g Gomega) {
		got := read(g, request)
		got.Annotations = map[string]string{"test/poke": metav1.Now().String()}
		g.Expect(k8s.Update(ctx, got)).To(Succeed())
	}, timeout).Should(Succeed())
}

func contractOf(g Gomega, request *pluginsv1alpha1.PluginRequest) *v1.ObjectStorageConfig {
	var osc v1.ObjectStorageConfig
	g.Expect(k8s.Get(ctx, client.ObjectKey{Namespace: request.Namespace, Name: request.Name}, &osc)).To(Succeed())
	return &osc
}

func eventReasons(g Gomega, namespace string) []string {
	var events corev1.EventList
	g.Expect(k8s.List(ctx, &events, client.InNamespace(namespace))).To(Succeed())
	reasons := make([]string, 0, len(events.Items))
	for _, e := range events.Items {
		reasons = append(reasons, e.Reason)
	}
	return reasons
}

// fill puts n objects into the bucket name.
func fill(name string, n int) {
	keys := make([]string, n)
	for i := range keys {
		keys[i] = fmt.Sprintf("zeebe/%05d", i)
	}
	fake.PutObjects(name, keys...)
}

// takeName creates the bucket of the claim orders in namespace, as a
// request with the UID uid of another Kubernetes cluster did.
func takeName(namespace, uid string) string {
	GinkgoHelper()
	name := bucketNameOf(namespace)
	_, err := fake.In(home).CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String(name),
		CreateBucketConfiguration: &types.CreateBucketConfiguration{
			LocationConstraint: home,
			Tags: []types.Tag{
				{Key: aws.String(awskit.TagNamespace), Value: aws.String(namespace)},
				{Key: aws.String(awskit.TagRequest), Value: aws.String("orders-backup-storage")},
				{Key: aws.String(awskit.TagRequestUID), Value: aws.String(uid)},
			},
		},
	})
	Expect(err).NotTo(HaveOccurred())
	return name
}

const bucketGrant = `{"Effect":"Allow","Action":["s3:ListBucket","s3:GetBucketLocation",` +
	`"s3:ListBucketMultipartUploads","s3:ListBucketVersions","s3:GetObject","s3:PutObject","s3:DeleteObject",` +
	`"s3:AbortMultipartUpload","s3:ListMultipartUploadParts"],"Resource":["arn:aws:s3:::%[1]s","arn:aws:s3:::%[1]s/*"]}`

var _ = Describe("the AWS S3 plugin", func() {
	It("creates the bucket, the contract, and both grants for a request with a key", func() {
		ns := newNamespace()
		request := bucketRequest(ns, map[string]string{"key": keyARN, "principal": roleARN},
			pluginsv1alpha1.DeletionPolicyDelete)
		readyRequest(request)
		name := bucketNameOf(ns)

		Eventually(func(g Gomega) {
			got := read(g, request)
			bucket := bucketOf(g, name)
			g.Expect(bucket.Tags).To(HaveKeyWithValue(awskit.TagRequestUID, string(got.UID)))
			g.Expect(bucket.Tags).To(HaveKeyWithValue("team", "platform"))
			g.Expect(bucket.Encryption.ApplyServerSideEncryptionByDefault.SSEAlgorithm).
				To(Equal(types.ServerSideEncryptionAwsKms))
			g.Expect(aws.ToString(bucket.Encryption.ApplyServerSideEncryptionByDefault.KMSMasterKeyID)).
				To(Equal(keyARN))
			g.Expect(aws.ToBool(bucket.Encryption.BucketKeyEnabled)).To(BeTrue())
			g.Expect(bucket.LifecycleRules).To(ConsistOf(HaveField("ID", HaveValue(Equal("abort-incomplete-uploads")))))

			g.Expect(got.Status.Outputs).To(Equal(map[string]string{
				"objectStorageConfig": request.Name,
				"bucket":              name,
				"bucketArn":           "arn:aws:s3:::" + name,
				"keyArn":              keyARN,
			}))
			g.Expect(got.Status.Summary).To(ContainElements(
				HaveField("Value", name),
				SatisfyAll(HaveField("Label", "Stored size (S3 Standard)"), HaveField("Value", "Not known yet")),
				HaveField("Value", "SSE-KMS with an S3 Bucket Key, key "+keyARN),
				SatisfyAll(HaveField("Label", "Region"), HaveField("Value", home)),
			))

			osc := contractOf(g, request)
			g.Expect(osc.Spec.S3.BucketName).To(Equal(name))
			g.Expect(osc.Spec.S3.Region).To(Equal(home))
			g.Expect(osc.Spec.S3.Auth.WorkloadIdentity).To(Equal(&v1.S3WorkloadIdentity{RoleARN: roleARN}))

			fragments := map[string]string{}
			for _, grant := range grantsOf(g, request) {
				fragments[grant.Spec.Template] = string(grant.Spec.Fragment.Raw)
			}
			g.Expect(fragments).To(HaveLen(2))
			g.Expect(fragments["bucket-read-write"]).To(MatchJSON(fmt.Sprintf(bucketGrant, name)))
			g.Expect(fragments["bucket-key-use"]).To(MatchJSON(
				`{"Effect":"Allow","Action":["kms:GenerateDataKey","kms:Decrypt"],"Resource":"` + keyARN + `"}`,
			))
		}, timeout).Should(Succeed())
	})

	It("gives a request without a key SSE-S3 and only the bucket grant", func() {
		ns := newNamespace()
		request := bucketRequest(ns, nil, pluginsv1alpha1.DeletionPolicyDelete)
		readyRequest(request)

		Eventually(func(g Gomega) {
			got := read(g, request)
			g.Expect(got.Status.Outputs).NotTo(HaveKey("keyArn"))
			g.Expect(grantsOf(g, request)).To(ConsistOf(HaveField("Spec.Template", "bucket-read-write")))
			bucket := bucketOf(g, bucketNameOf(ns))
			g.Expect(bucket.Encryption.ApplyServerSideEncryptionByDefault.SSEAlgorithm).
				To(Equal(types.ServerSideEncryptionAes256))
			g.Expect(contractOf(g, request).Spec.S3.Auth.WorkloadIdentity).To(BeNil())
		}, timeout).Should(Succeed())
	})

	It("refuses a bucket name of another request", func() {
		ns := newNamespace()
		takeName(ns, "uid-of-another-cluster")
		request := bucketRequest(ns, nil, pluginsv1alpha1.DeletionPolicyDelete)
		Expect(k8s.Create(ctx, request)).To(Succeed())

		Eventually(func(g Gomega) {
			c := conditionOf(g, request, pluginsv1alpha1.ConditionOutputsReady)
			g.Expect(c.Status).To(Equal(metav1.ConditionFalse))
			g.Expect(c.Reason).To(Equal(awss3.ReasonBucketNameInUse))
			g.Expect(c.Message).To(ContainSubstring("the request " + ns + "/orders-backup-storage with the UID " +
				"uid-of-another-cluster"))
			g.Expect(conditionOf(g, request, v1.ConditionReady).Reason).To(Equal(awss3.ReasonBucketNameInUse))
		}, timeout).Should(Succeed())
		Consistently(func(g Gomega) { g.Expect(grantsOf(g, request)).To(BeEmpty()) }).Should(Succeed())

		Expect(k8s.Delete(ctx, request)).To(Succeed())
		Eventually(gone(request), timeout).Should(BeTrue())
		Expect(bucketOf(Default, bucketNameOf(ns)).Tags).To(HaveKeyWithValue(awskit.TagRequestUID,
			"uid-of-another-cluster"), "the delete keeps the bucket of the other request")
	})

	It("refuses a bucket name of another AWS account", func() {
		ns := newNamespace()
		fake.AddForeignBucket(bucketNameOf(ns), home)
		request := bucketRequest(ns, nil, pluginsv1alpha1.DeletionPolicyDelete)
		Expect(k8s.Create(ctx, request)).To(Succeed())

		Eventually(func(g Gomega) {
			c := conditionOf(g, request, pluginsv1alpha1.ConditionOutputsReady)
			g.Expect(c.Reason).To(Equal(awss3.ReasonBucketNameInUse))
			g.Expect(c.Message).To(ContainSubstring("another AWS account"))
		}, timeout).Should(Succeed())

		Expect(k8s.Delete(ctx, request)).To(Succeed())
		Eventually(gone(request), timeout).Should(BeTrue())
	})

	It("keeps the record of its bucket when the bucket gets another UID", func() {
		ns := newNamespace()
		request := bucketRequest(ns, nil, pluginsv1alpha1.DeletionPolicyDelete)
		readyRequest(request)
		name := bucketNameOf(ns)
		fake.Edit(name, func(b *awsfake.Bucket) { b.Tags[awskit.TagRequestUID] = "uid-of-another-cluster" })

		poke(request)

		Eventually(func(g Gomega) {
			g.Expect(conditionOf(g, request, pluginsv1alpha1.ConditionOutputsReady).Reason).
				To(Equal(awss3.ReasonBucketNameInUse))
			g.Expect(read(g, request).Status.Resources).To(ContainElement(SatisfyAll(
				HaveField("Kind", "S3Bucket"), HaveField("Name", name), HaveField("Location", home),
			)))
		}, timeout).Should(Succeed())
	})

	It("binds the bucket name before it creates the bucket", func() {
		ns := newNamespace()
		name := bucketNameOf(ns)
		fake.FailCreate(name, &smithy.GenericAPIError{Code: "AccessDenied", Message: "The proof refuses it"})
		DeferCleanup(func() { fake.FailCreate(name, nil) })
		request := bucketRequest(ns, nil, pluginsv1alpha1.DeletionPolicyDelete)
		Expect(k8s.Create(ctx, request)).To(Succeed())

		Eventually(func(g Gomega) {
			g.Expect(conditionOf(g, request, v1.ConditionReady).Reason).To(Equal(pluginkit.ReasonReconcileError))
			g.Expect(read(g, request).Status.Resources).To(ContainElement(SatisfyAll(
				HaveField("Kind", "S3Bucket"), HaveField("Name", name),
			)))
		}, timeout).Should(Succeed())
		_, created := fake.Bucket(name)
		Expect(created).To(BeFalse())

		// A create that failed can still have made the bucket, so the name
		// stays bound.
		Eventually(func(g Gomega) {
			got := read(g, request)
			got.Spec.Parameters = raw(`{"bucketPrefix":"other"}`)
			g.Expect(k8s.Update(ctx, got)).To(Succeed())
		}, timeout).Should(Succeed())
		Eventually(func(g Gomega) {
			c := conditionOf(g, request, pluginsv1alpha1.ConditionOutputsReady)
			g.Expect(c.Reason).To(Equal(pluginsv1alpha1.ReasonInvalidParameters))
			g.Expect(c.Message).To(ContainSubstring(name))
		}, timeout).Should(Succeed())

		fake.FailCreate(name, nil)
		Eventually(func(g Gomega) {
			got := read(g, request)
			got.Spec.Parameters = nil
			g.Expect(k8s.Update(ctx, got)).To(Succeed())
		}, timeout).Should(Succeed())
		Eventually(func(g Gomega) {
			readyGrants(g, request)
			g.Expect(conditionOf(g, request, v1.ConditionReady).Status).To(Equal(metav1.ConditionTrue))
			g.Expect(read(g, request).Status.Resources).To(ContainElement(SatisfyAll(
				HaveField("Kind", "S3Bucket"), HaveField("ID", "arn:aws:s3:::"+name),
			)))
		}, timeout).Should(Succeed())
	})

	It("waits for the tags of a new bucket that S3 shows late, also in a delete", func() {
		fake.ShowLate("GetBucketTagging", -1)
		DeferCleanup(func() { fake.ShowLate("GetBucketTagging", 0) })
		ns := newNamespace()
		name := bucketNameOf(ns)
		request := bucketRequest(ns, nil, pluginsv1alpha1.DeletionPolicyDelete)
		Expect(k8s.Create(ctx, request)).To(Succeed())
		Eventually(func(g Gomega) {
			c := conditionOf(g, request, pluginsv1alpha1.ConditionOutputsReady)
			g.Expect(c.Reason).To(Equal(awss3.ReasonWaitingForBucketTags))
			g.Expect(c.Message).To(ContainSubstring(name))
		}, timeout).Should(Succeed())

		Eventually(func(g Gomega) {
			got := read(g, request)
			got.Spec.Parameters = raw(`{"bucketPrefix":"other"}`)
			g.Expect(k8s.Update(ctx, got)).To(Succeed())
		}, timeout).Should(Succeed())
		Eventually(func(g Gomega) {
			c := conditionOf(g, request, pluginsv1alpha1.ConditionOutputsReady)
			g.Expect(c.Reason).To(Equal(pluginsv1alpha1.ReasonInvalidParameters))
		}, timeout).Should(Succeed())
		Expect(bucketsOf(ns)).To(HaveLen(1), "no second bucket")

		Expect(k8s.Delete(ctx, request)).To(Succeed())
		Eventually(func(g Gomega) {
			c := conditionOf(g, request, v1.ConditionReady)
			g.Expect(c.Reason).To(Equal(pluginkit.ReasonDeleting))
			g.Expect(c.Message).To(Equal("Waiting for S3 to show the tags of the new bucket " + name))
		}, timeout).Should(Succeed())
		_, kept := fake.Bucket(name)
		Expect(kept).To(BeTrue(), "the plugin deletes no bucket without its UID tag")

		fake.ShowLate("GetBucketTagging", 0)
		Eventually(gone(request), timeout).Should(BeTrue())
		_, kept = fake.Bucket(name)
		Expect(kept).To(BeFalse())
	})

	It("keeps the contract of an orphaned bucket when a new request of the claim is deleted", func() {
		ns := newNamespace()
		orphaned := bucketRequest(ns, nil, pluginsv1alpha1.DeletionPolicyOrphan)
		readyRequest(orphaned)
		Expect(k8s.Delete(ctx, orphaned)).To(Succeed())
		Eventually(gone(orphaned), timeout).Should(BeTrue())

		again := bucketRequest(ns, nil, pluginsv1alpha1.DeletionPolicyDelete)
		Expect(k8s.Create(ctx, again)).To(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(conditionOf(g, again, pluginsv1alpha1.ConditionOutputsReady).Reason).
				To(Equal(awss3.ReasonBucketNameInUse))
		}, timeout).Should(Succeed())
		Expect(k8s.Delete(ctx, again)).To(Succeed())
		Eventually(gone(again), timeout).Should(BeTrue())

		Expect(contractOf(Default, orphaned).Spec.S3.BucketName).To(Equal(bucketNameOf(ns)))
		_, kept := fake.Bucket(bucketNameOf(ns))
		Expect(kept).To(BeTrue())
	})

	It("keeps the bucket when the region parameter changes", func() {
		ns := newNamespace()
		request := bucketRequest(ns, nil, pluginsv1alpha1.DeletionPolicyDelete)
		readyRequest(request)

		Eventually(func(g Gomega) {
			got := read(g, request)
			got.Spec.Parameters = raw(`{"region":"eu-central-1"}`)
			g.Expect(k8s.Update(ctx, got)).To(Succeed())
		}, timeout).Should(Succeed())

		Eventually(func(g Gomega) {
			c := conditionOf(g, request, pluginsv1alpha1.ConditionOutputsReady)
			g.Expect(c.Reason).To(Equal(pluginsv1alpha1.ReasonInvalidParameters))
			g.Expect(c.Message).To(ContainSubstring(bucketNameOf(ns) + " in " + home))
			g.Expect(read(g, request).Status.Resources).To(ContainElement(HaveField("Kind", "S3Bucket")))
		}, timeout).Should(Succeed())
		Expect(bucketsOf(ns)).To(HaveLen(1), "no second bucket")

		Expect(k8s.Delete(ctx, request)).To(Succeed())
		Eventually(gone(request), timeout).Should(BeTrue())
		Expect(bucketsOf(ns)).To(BeEmpty(), "the delete finds the bucket by its recorded row")
	})

	It("repairs a public access block that was turned off", func() {
		ns := newNamespace()
		request := bucketRequest(ns, nil, pluginsv1alpha1.DeletionPolicyDelete)
		readyRequest(request)
		name := bucketNameOf(ns)
		fake.Edit(name, func(b *awsfake.Bucket) { b.PublicAccessBlock.BlockPublicPolicy = aws.Bool(false) })

		poke(request)

		Eventually(func(g Gomega) {
			g.Expect(aws.ToBool(bucketOf(g, name).PublicAccessBlock.BlockPublicPolicy)).To(BeTrue())
			g.Expect(eventReasons(g, ns)).To(ContainElement("DriftCorrected"))
		}, timeout).Should(Succeed())
	})

	It("reports no drift for the rule of a new bucket that S3 shows late", func() {
		fake.ShowLate("GetBucketLifecycleConfiguration", -1)
		DeferCleanup(func() { fake.ShowLate("GetBucketLifecycleConfiguration", 0) })
		ns := newNamespace()
		request := bucketRequest(ns, nil, pluginsv1alpha1.DeletionPolicyDelete)
		readyRequest(request)

		poke(request)

		Consistently(func(g Gomega) {
			reasons := eventReasons(g, ns)
			g.Expect(reasons).NotTo(ContainElement("DriftCorrected"))
			g.Expect(slices.DeleteFunc(reasons, func(r string) bool { return r != "LifecycleUpdated" })).
				To(HaveLen(1), "the plugin writes the rule once")
			g.Expect(conditionOf(g, request, v1.ConditionReady).Status).To(Equal(metav1.ConditionTrue))
		}, 3*time.Second).Should(Succeed())
	})

	It("deletes a bucket with objects and its contract in more than one pass", func() {
		ns := newNamespace()
		request := bucketRequest(ns, nil, pluginsv1alpha1.DeletionPolicyDelete)
		readyRequest(request)
		name := bucketNameOf(ns)
		fill(name, 2500)

		Expect(k8s.Delete(ctx, request)).To(Succeed())

		Eventually(func(g Gomega) {
			c := conditionOf(g, request, v1.ConditionReady)
			g.Expect(c.Reason).To(Equal(pluginkit.ReasonDeleting))
			g.Expect(c.Message).To(Equal("Deleting the objects of the bucket " + name))
		}, timeout).Should(Succeed())
		Eventually(gone(request), timeout).Should(BeTrue())
		_, ok := fake.Bucket(name)
		Expect(ok).To(BeFalse())
		err := k8s.Get(ctx, client.ObjectKey{Namespace: ns, Name: request.Name}, &v1.ObjectStorageConfig{})
		Expect(apierrors.IsNotFound(err)).To(BeTrue(), "the contract is gone")
	})

	It("goes on with the delete of a bucket after a restart of the plugin", func() {
		ns := newNamespace()
		request := bucketRequest(ns, nil, pluginsv1alpha1.DeletionPolicyDelete)
		readyRequest(request)
		name := bucketNameOf(ns)
		fill(name, 4000)

		Expect(k8s.Delete(ctx, request)).To(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(conditionOf(g, request, v1.ConditionReady).Reason).To(Equal(pluginkit.ReasonDeleting))
		}, timeout).Should(Succeed())
		stopMgr()

		Expect(bucketOf(Default, name).Objects).NotTo(BeEmpty(), "the plugin stopped between two passes")
		startManager("aws-s3-restarted")

		Eventually(gone(request), timeout).Should(BeTrue())
		_, ok := fake.Bucket(name)
		Expect(ok).To(BeFalse())
	})

	It("deletes a bucket whose versioning someone turned on", func() {
		ns := newNamespace()
		request := bucketRequest(ns, nil, pluginsv1alpha1.DeletionPolicyDelete)
		readyRequest(request)
		name := bucketNameOf(ns)
		fill(name, 5)
		fake.Edit(name, func(b *awsfake.Bucket) {
			b.Versioning = types.BucketVersioningStatusEnabled
			b.Versions = []awsfake.ObjectVersion{
				{Key: "zeebe/00001", VersionID: "v1"},
				{Key: "zeebe/old", VersionID: "v2", DeleteMarker: true},
			}
		})

		Expect(k8s.Delete(ctx, request)).To(Succeed())

		Eventually(gone(request), timeout).Should(BeTrue())
		_, ok := fake.Bucket(name)
		Expect(ok).To(BeFalse())
	})

	It("orphans the bucket and the contract", func() {
		ns := newNamespace()
		request := bucketRequest(ns, map[string]string{"key": keyARN}, pluginsv1alpha1.DeletionPolicyOrphan)
		readyRequest(request)
		name := bucketNameOf(ns)
		fill(name, 2)
		uid := read(Default, request).UID

		Expect(k8s.Delete(ctx, request)).To(Succeed())

		Eventually(gone(request), timeout).Should(BeTrue())
		bucket := bucketOf(Default, name)
		Expect(bucket.Objects).To(HaveLen(2))
		Expect(bucket.Tags).To(Equal(map[string]string{"team": "platform"}))
		osc := contractOf(Default, request)
		for _, ref := range osc.OwnerReferences {
			Expect(ref.UID).NotTo(Equal(uid))
		}
		Expect(strings.Join(eventReasons(Default, ns), ",")).To(ContainSubstring("BucketReleased"))
	})
})
