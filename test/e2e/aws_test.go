//go:build e2e

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

package e2e

import (
	"path"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fleetv1alpha1 "github.com/konsole-is/camunda-operator/api/fleet/v1alpha1"
	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
	v1 "github.com/konsole-is/camunda-operator/api/v1"
)

const (
	// awsSet is the name of the Environment of the journey and of its
	// namespace.
	awsSet = "aws"

	sectionCloudIdentity = "cloud-identity"
	sectionEncryptionKey = "encryption-key"

	registrationIdentity = "aws-irsa"
	registrationKMSKey   = "aws-kms-key"
	registrationS3       = "aws-s3"

	outputPrincipal = "principal"
	outputKeyRef    = "keyRef"
	outputBucket    = "bucket"
	outputKeyARN    = "keyArn"

	// The ServiceAccount of the reader role that test/e2e/aws/setup.sh
	// creates.
	readerNamespace      = "aws-reader"
	readerServiceAccount = "aws-reader"

	requestsReadyTimeout = 10 * time.Minute
	// awsSettleTimeout is the time that IAM and S3 can still answer for a
	// deleted object.
	awsSettleTimeout = 2 * time.Minute
)

var _ = Describe("the AWS journey", Ordered, Label(labelAWS), func() {
	var (
		claim  fleetv1alpha1.CamundaClusterClaim
		role   string
		key    string
		bucket string
	)

	BeforeAll(func() {
		createAll(
			basicPlatform(fixtureName),
			release(fixtureName),
			clusterPreset(fixtureName),
			elasticsearchPreset(fixtureName),
			awsEnvironment(requiredEnv("RUN_ID")),
			environmentNamespace(awsSet, awsSet),
			awsClaim(),
		)
	})

	It("gives the claim a role, a key, and a bucket that holds the key and trusts the role", func() {
		claimKey := client.ObjectKey{Namespace: awsSet, Name: claimName}
		Eventually(func(g Gomega) {
			expectSectionsReady(g, claimKey, sectionCloudIdentity, sectionEncryptionKey, sectionBackupStorage)
		}, requestsReadyTimeout, 10*time.Second).Should(Succeed())

		Expect(k8s.Get(ctx, claimKey, &claim)).To(Succeed())
		requests, err := claimRequests(&claim)
		Expect(err).NotTo(HaveOccurred())
		identity := requestOf(requests, &claim, sectionCloudIdentity)
		encryption := requestOf(requests, &claim, sectionEncryptionKey)
		storage := requestOf(requests, &claim, sectionBackupStorage)
		role = identity.Status.Outputs[outputPrincipal]
		key = encryption.Status.Outputs[outputKeyRef]
		bucket = storage.Status.Outputs[outputBucket]
		Expect(storage.Status.Outputs[outputKeyARN]).To(Equal(key))

		var config v1.ObjectStorageConfig
		configKey := client.ObjectKey{Namespace: awsSet, Name: storage.Status.Outputs[outputObjectStorageConfig]}
		Expect(k8s.Get(ctx, configKey, &config)).To(Succeed())
		Expect(config.Spec.S3).NotTo(BeNil())
		Expect(config.Spec.S3.BucketName).To(Equal(bucket))
		Expect(config.Spec.S3.Auth.WorkloadIdentity).NotTo(BeNil())
		Expect(config.Spec.S3.Auth.WorkloadIdentity.RoleARN).To(Equal(role))

		// The reader sees each object, so a refused read later never reads as
		// a deleted object.
		Expect(readAWS("iam", "get-role", "--role-name", path.Base(role), "--query", "Role.Arn", "--output", "text")).
			To(WithTransform(strings.TrimSpace, Equal(role)))
		Expect(readAWS("s3api", "head-bucket", "--bucket", bucket)).Error().NotTo(HaveOccurred())
		Expect(readAWS("kms", "describe-key", "--key-id", key, "--query", "KeyMetadata.KeyState", "--output", "text")).
			To(WithTransform(strings.TrimSpace, Equal("Enabled")))
	})

	It(
		"removes the role and the bucket, and schedules the deletion of the key, when the claim is deleted with Delete",
		func() {
			Expect(k8s.Delete(ctx, &claim)).To(Succeed())
			Eventually(func(g Gomega) { expectClaimGone(g, client.ObjectKeyFromObject(&claim)) },
				deletionTimeout, 10*time.Second).Should(Succeed())

			Eventually(func(g Gomega) {
				out, err := readAWS("iam", "get-role", "--role-name", path.Base(role))
				g.Expect(err).To(HaveOccurred(), "the role %s is still there:\n%s", role, out)
				g.Expect(out).To(ContainSubstring("NoSuchEntity"))
			}, awsSettleTimeout, 15*time.Second).Should(Succeed())
			Eventually(func(g Gomega) {
				out, err := readAWS("s3api", "head-bucket", "--bucket", bucket)
				g.Expect(err).To(HaveOccurred(), "the bucket %s is still there:\n%s", bucket, out)
				g.Expect(out).To(ContainSubstring("(404)"))
			}, awsSettleTimeout, 15*time.Second).Should(Succeed())
			Expect(
				readAWS("kms", "describe-key", "--key-id", key, "--query", "KeyMetadata.KeyState", "--output", "text"),
			).
				To(WithTransform(strings.TrimSpace, Equal("PendingDeletion")))
		},
	)
})

// awsEnvironment returns the Environment of the journey. Each section
// requires its one registration, so the claim gets exactly the plugins of
// the AWS set. runID is the run in the AWS sandbox.
func awsEnvironment(runID string) *fleetv1alpha1.Environment {
	return &fleetv1alpha1.Environment{
		ObjectMeta: metav1.ObjectMeta{Name: awsSet},
		Spec: fleetv1alpha1.EnvironmentSpec{
			PlatformConfigRef: fixtureName,
			Presets: fleetv1alpha1.EnvironmentPresets{
				CamundaCluster:       []string{fixtureName},
				ElasticsearchCluster: []string{fixtureName},
			},
			Releases: &fleetv1alpha1.EnvironmentReleases{Allowed: []string{fixtureName}, Default: fixtureName},
			Sections: map[string]fleetv1alpha1.SectionPolicy{
				sectionCloudIdentity:    onlyRegistration(registrationIdentity),
				sectionEncryptionKey:    onlyRegistration(registrationKMSKey),
				sectionBackupStorage:    onlyRegistration(registrationS3),
				sectionSecondaryStorage: onlyRegistration(registrationElasticsearch),
			},
			Parameters: map[string]fleetv1alpha1.ParameterPolicies{
				registrationElasticsearch: {"preset": fixedParameter(fixtureName)},
				// The sandbox refuses every other deletion window of a key.
				registrationKMSKey: {"pendingWindowDays": fixedParameter(7)},
				// The controller role of aws-s3 and the sweep of the sandbox
				// know only the buckets of the run.
				registrationS3: {"bucketPrefix": fixedParameter(runID)},
			},
		},
	}
}

func awsClaim() *fleetv1alpha1.CamundaClusterClaim {
	return &fleetv1alpha1.CamundaClusterClaim{
		ObjectMeta: metav1.ObjectMeta{Namespace: awsSet, Name: claimName},
		Spec: fleetv1alpha1.CamundaClusterClaimSpec{
			PresetRef: fixtureName,
			Plugins: []fleetv1alpha1.PluginSelection{
				{Section: sectionCloudIdentity, Registration: registrationIdentity},
				{Section: sectionEncryptionKey, Registration: registrationKMSKey},
				{Section: sectionBackupStorage, Registration: registrationS3},
				{Section: sectionSecondaryStorage, Registration: registrationElasticsearch},
			},
			DeletionPolicy: pluginsv1alpha1.DeletionPolicyDelete,
		},
	}
}

// readAWS runs the AWS CLI with args as the reader role of the run, and
// returns its output. A command that fails is an error that holds its
// output.
func readAWS(args ...string) (string, error) {
	env := []corev1.EnvVar{
		{Name: "AWS_REGION", Value: requiredEnv("AWS_REGION")},
		{Name: "AWS_EC2_METADATA_DISABLED", Value: "true"},
	}

	return awsCLI(readerNamespace, readerServiceAccount, env, args...)
}
