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
	"fmt"
	"os/exec"
	"path"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fleetv1alpha1 "github.com/konsole-is/camunda-operator/api/fleet/v1alpha1"
	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
	v1 "github.com/konsole-is/camunda-operator/api/v1"
)

const (
	// eksSet is the name of the Environment of the eks journey and of its
	// namespace.
	eksSet = "eks"

	sectionVolumeEncryption = "volume-encryption"

	registrationPodIdentity = "aws-pod-identity"
	registrationEBS         = "aws-ebs-encrypted"

	outputStorageClassName = "storageClassName"

	ebsDriver = "ebs.csi.aws.com"
	// untrustedServiceAccount is a ServiceAccount of the namespace of the
	// cluster that no request names.
	untrustedServiceAccount = "untrusted"

	// credentialsTimeout is the time that a new Pod Identity association can
	// take before the pods of its ServiceAccount get credentials.
	credentialsTimeout = 3 * time.Minute
)

var _ = Describe("the EKS proof", Ordered, Label(labelEKS), func() {
	var (
		claim    fleetv1alpha1.CamundaClusterClaim
		identity *pluginsv1alpha1.PluginRequest
		class    string
	)
	BeforeAll(func() {
		createAll(
			basicPlatform(fixtureName),
			release(fixtureName),
			clusterPreset(fixtureName),
			elasticsearchPreset(fixtureName),
			eksEnvironment(),
			environmentNamespace(eksSet, eksSet),
			eksClaim(),
		)
	})

	It("makes the claim Ready with a broker volume that EBS encrypts with the key of the claim", func() {
		claimKey := client.ObjectKey{Namespace: eksSet, Name: claimName}
		Eventually(
			func(g Gomega) { expectClaimReady(g, claimKey) },
			claimReadyTimeout,
			10*time.Second,
		).Should(Succeed())

		Expect(k8s.Get(ctx, claimKey, &claim)).To(Succeed())
		requests, err := claimRequests(&claim)
		Expect(err).NotTo(HaveOccurred())
		identity = requestOf(requests, &claim, sectionCloudIdentity)
		Expect(identity).NotTo(BeNil())
		key := requestOf(requests, &claim, sectionEncryptionKey).Status.Outputs[outputKeyRef]
		class = requestOf(requests, &claim, sectionVolumeEncryption).Status.Outputs[outputStorageClassName]
		Expect(key).NotTo(BeEmpty())
		Expect(class).NotTo(BeEmpty())

		var cluster v1.CamundaCluster
		Expect(claim.Status.Cluster).NotTo(BeNil())
		Expect(k8s.Get(ctx, client.ObjectKey{Namespace: eksSet, Name: claim.Status.Cluster.Name}, &cluster)).
			To(Succeed())
		Expect(cluster.Spec.Zeebe).NotTo(BeNil())
		Expect(cluster.Spec.Zeebe.StorageClassName).To(HaveValue(Equal(class)))

		volumes, err := brokerVolumeIDs(eksSet, class)
		Expect(err).NotTo(HaveOccurred())
		for _, volume := range volumes {
			Expect(awsOnRunner(
				"ec2", "describe-volumes", "--volume-ids", volume,
				"--query", "Volumes[0].[Encrypted, KmsKeyId]", "--output", "text",
			)).To(WithTransform(strings.Fields, Equal([]string{"True", key})), "the volume %s", volume)
		}
	})

	It("gives a pod under the ServiceAccount of the cluster the role of the identity hub", func() {
		Expect(identity).NotTo(BeNil(), "the claim has no request of the section %s", sectionCloudIdentity)
		role := identity.Status.Outputs[outputPrincipal]
		serviceAccount := identity.Spec.Target.ServiceAccountName
		Expect(role).NotTo(BeEmpty())
		Expect(serviceAccount).NotTo(BeEmpty())

		// EKS can take a while before a new association gives credentials, so
		// each try starts a new pod.
		Eventually(func(g Gomega) {
			out, err := awsCLI(eksSet, serviceAccount, regionEnv(), "sts", "get-caller-identity", "--query", "Arn", "--output", "text")
			g.Expect(err).NotTo(HaveOccurred(), out)
			g.Expect(strings.TrimSpace(out)).To(HavePrefix(
				"arn:aws:sts::" + requiredEnv("AWS_ACCOUNT_ID") + ":assumed-role/" + path.Base(role) + "/",
			))
		}, credentialsTimeout, 10*time.Second).Should(Succeed())
	})

	// The node has a metadata hop limit of 1, so a pod without an association
	// does not get the role of the node either.
	It("gives a pod under a ServiceAccount that the request does not name no credentials", func() {
		Expect(identity).NotTo(BeNil(), "the claim has no request of the section %s", sectionCloudIdentity)
		target := identity.Spec.Target
		Expect(append([]string{target.ServiceAccountName}, target.AdditionalServiceAccounts...)).
			NotTo(ContainElement(untrustedServiceAccount))
		createAll(&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: untrustedServiceAccount, Namespace: eksSet}})

		out, err := awsCLI(eksSet, untrustedServiceAccount, regionEnv(), "sts", "get-caller-identity")
		Expect(err).To(HaveOccurred(), "the pod got credentials:\n%s", out)
		Expect(out).To(ContainSubstring("Unable to locate credentials"))
	})

	It("removes the StorageClass of the claim when the claim is deleted with Delete", func() {
		Expect(class).NotTo(BeEmpty(), "the claim has no StorageClass")
		Expect(k8s.Delete(ctx, &claim)).To(Succeed())
		Eventually(func(g Gomega) { expectClaimGone(g, client.ObjectKeyFromObject(&claim)) },
			deletionTimeout, 10*time.Second).Should(Succeed())

		Eventually(func(g Gomega) {
			err := k8s.Get(ctx, client.ObjectKey{Name: class}, &storagev1.StorageClass{})
			g.Expect(err).To(Satisfy(apierrors.IsNotFound), "the StorageClass %s is still there", class)
		}, deletionTimeout, 10*time.Second).Should(Succeed())
	})
})

// eksEnvironment returns the Environment of the eks journey. Each section
// requires its one registration, so the claim gets exactly the plugins of
// the proof.
func eksEnvironment() *fleetv1alpha1.Environment {
	return &fleetv1alpha1.Environment{
		ObjectMeta: metav1.ObjectMeta{Name: eksSet},
		Spec: fleetv1alpha1.EnvironmentSpec{
			PlatformConfigRef: fixtureName,
			Presets: fleetv1alpha1.EnvironmentPresets{
				CamundaCluster:       []string{fixtureName},
				ElasticsearchCluster: []string{fixtureName},
			},
			Releases: &fleetv1alpha1.EnvironmentReleases{Allowed: []string{fixtureName}, Default: fixtureName},
			Sections: map[string]fleetv1alpha1.SectionPolicy{
				sectionCloudIdentity:    onlyRegistration(registrationPodIdentity),
				sectionEncryptionKey:    onlyRegistration(registrationKMSKey),
				sectionVolumeEncryption: onlyRegistration(registrationEBS),
				sectionSecondaryStorage: onlyRegistration(registrationElasticsearch),
			},
			Parameters: map[string]fleetv1alpha1.ParameterPolicies{
				registrationElasticsearch: {"preset": fixedParameter(fixtureName)},
				// The sandbox refuses every other deletion window of a key.
				registrationKMSKey: {"pendingWindowDays": fixedParameter(7)},
			},
		},
	}
}

func eksClaim() *fleetv1alpha1.CamundaClusterClaim {
	return &fleetv1alpha1.CamundaClusterClaim{
		ObjectMeta: metav1.ObjectMeta{Namespace: eksSet, Name: claimName},
		Spec: fleetv1alpha1.CamundaClusterClaimSpec{
			PresetRef: fixtureName,
			Plugins: []fleetv1alpha1.PluginSelection{
				{Section: sectionCloudIdentity, Registration: registrationPodIdentity},
				{Section: sectionEncryptionKey, Registration: registrationKMSKey},
				{Section: sectionVolumeEncryption, Registration: registrationEBS},
				{Section: sectionSecondaryStorage, Registration: registrationElasticsearch},
			},
			DeletionPolicy: pluginsv1alpha1.DeletionPolicyDelete,
		},
	}
}

// brokerVolumeIDs returns the EBS volume ID of each PersistentVolumeClaim of
// namespace with the StorageClass storageClass. A namespace without such a
// claim, a claim that is not Bound, and a volume of another driver than the
// EBS CSI driver are errors.
func brokerVolumeIDs(namespace, storageClass string) ([]string, error) {
	var claims corev1.PersistentVolumeClaimList
	if err := k8s.List(ctx, &claims, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("listing the PersistentVolumeClaims of %s: %w", namespace, err)
	}

	var ids []string
	for _, pvc := range claims.Items {
		if pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName != storageClass {
			continue
		}
		if pvc.Status.Phase != corev1.ClaimBound {
			return nil, fmt.Errorf("the PersistentVolumeClaim %s/%s is %s, not Bound", namespace, pvc.Name, pvc.Status.Phase)
		}

		var volume corev1.PersistentVolume
		if err := k8s.Get(ctx, client.ObjectKey{Name: pvc.Spec.VolumeName}, &volume); err != nil {
			return nil, fmt.Errorf("reading the PersistentVolume of %s/%s: %w", namespace, pvc.Name, err)
		}
		if volume.Spec.CSI == nil || volume.Spec.CSI.Driver != ebsDriver {
			return nil, fmt.Errorf("the PersistentVolume %s is not a volume of %s", volume.Name, ebsDriver)
		}
		ids = append(ids, volume.Spec.CSI.VolumeHandle)
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("no PersistentVolumeClaim of %s has the StorageClass %s", namespace, storageClass)
	}

	return ids, nil
}

// regionEnv returns the environment of an AWS CLI pod of the journey. It
// leaves the instance metadata on, so that a pod without credentials shows
// that the node role stays out of reach.
func regionEnv() []corev1.EnvVar {
	return []corev1.EnvVar{{Name: "AWS_REGION", Value: requiredEnv("AWS_REGION")}}
}

// awsOnRunner runs the AWS CLI with args on the runner, with the credentials
// of the job, and returns its output. A command that fails is an error that
// holds its output.
func awsOnRunner(args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "aws", args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("aws %s: %w\n%s", strings.Join(args, " "), err, out)
	}

	return string(out), nil
}
