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
	"net/url"
	"slices"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fleetv1alpha1 "github.com/konsole-is/camunda-operator/api/fleet/v1alpha1"
	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
	v1 "github.com/konsole-is/camunda-operator/api/v1"
)

const (
	// bareMetal is the name of the Environment of the journey and of its
	// namespace.
	bareMetal = "bare-metal"
	claimName = "orders"
	// fixtureName names the platform config, the release, and both presets.
	fixtureName = "journey"
	backupName  = "orders-backup"

	sectionBackupStorage    = "backup-storage"
	sectionAccess           = "access"
	sectionSecondaryStorage = "secondary-storage"

	registrationRustFS        = "rustfs-bucket"
	registrationGateway       = "gateway-public"
	registrationElasticsearch = "elasticsearch"

	outputObjectStorageConfig = "objectStorageConfig"
	outputExternalURL         = "externalUrl"

	// The server and the admin key that test/kind/rustfs.sh writes.
	rustfsNamespace       = "rustfs"
	rustfsAdminSecret     = "rustfs-admin"
	rustfsAccessKeyKey    = "accessKey"
	rustfsSecretKeyKey    = "secretKey"
	rustfsEndpoint        = "http://rustfs:9000"
	envoyGatewayNamespace = "envoy-gateway-system"
	// envoySelector selects the Service of the Envoy proxy of the Gateway
	// gateway-system/public, as the Envoy Gateway quickstart selects it.
	envoySelector = "gateway.envoyproxy.io/owning-gateway-namespace=gateway-system," +
		"gateway.envoyproxy.io/owning-gateway-name=public"

	claimReadyTimeout = 20 * time.Minute
	operateTimeout    = 5 * time.Minute
	backupTimeout     = 15 * time.Minute
	deletionTimeout   = 10 * time.Minute
	podTimeout        = 2 * time.Minute
)

var _ = Describe("the bare-metal journey", Ordered, Label(labelBareMetal), func() {
	var (
		claim   fleetv1alpha1.CamundaClusterClaim
		cluster v1.CamundaCluster
		bucket  string
	)

	BeforeAll(func() {
		createAll(
			basicPlatform(fixtureName),
			release(fixtureName),
			clusterPreset(fixtureName),
			elasticsearchPreset(fixtureName),
			bareMetalEnvironment(),
			environmentNamespace(bareMetal, bareMetal),
			bareMetalClaim(),
		)
	})

	It("makes the cluster of the claim Ready, with the outputs of both plugins in its spec", func() {
		key := client.ObjectKey{Namespace: bareMetal, Name: claimName}
		Eventually(func(g Gomega) { expectClaimReady(g, key) }, claimReadyTimeout, 10*time.Second).Should(Succeed())

		Expect(k8s.Get(ctx, key, &claim)).To(Succeed())
		Expect(claim.Status.Cluster).NotTo(BeNil())
		Expect(k8s.Get(ctx, client.ObjectKey{Namespace: bareMetal, Name: claim.Status.Cluster.Name}, &cluster)).
			To(Succeed())
		Expect(meta.IsStatusConditionTrue(cluster.Status.Conditions, v1.ConditionReady)).To(BeTrue())

		requests, err := claimRequests(&claim)
		Expect(err).NotTo(HaveOccurred())
		backup := requestOf(requests, &claim, sectionBackupStorage)
		access := requestOf(requests, &claim, sectionAccess)
		Expect(backup).NotTo(BeNil())
		Expect(access).NotTo(BeNil())
		Expect(cluster.Spec.BackupStorageRef).To(Equal(backup.Status.Outputs[outputObjectStorageConfig]))
		Expect(cluster.Spec.ExternalURL).To(Equal(access.Status.Outputs[outputExternalURL]))

		var storage v1.ObjectStorageConfig
		Expect(k8s.Get(ctx, client.ObjectKey{Namespace: bareMetal, Name: cluster.Spec.BackupStorageRef}, &storage)).
			To(Succeed())
		Expect(storage.Spec.S3).NotTo(BeNil())
		bucket = storage.Spec.S3.BucketName
		Expect(bucket).NotTo(BeEmpty())
	})

	It("serves Operate at the external URL through the Gateway", func() {
		address, err := envoyAddress()
		Expect(err).NotTo(HaveOccurred())

		Eventually(func(g Gomega) {
			code, err := operateStatus(bareMetal, cluster.Name, cluster.Spec.ExternalURL, address)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(code).To(Equal("200"))
		}, operateTimeout, 10*time.Second).Should(Succeed())
	})

	It("completes a backup into the bucket of the rustfs-bucket plugin", func() {
		before, err := bucketKeys(bucket)
		Expect(err).NotTo(HaveOccurred())

		createAll(&v1.LogicalBackupElasticsearch{
			ObjectMeta: metav1.ObjectMeta{Name: backupName, Namespace: bareMetal},
			Spec:       v1.LogicalBackupElasticsearchSpec{ClusterRef: v1.ClusterRef{Name: cluster.Name}},
		})
		Eventually(func(g Gomega) {
			var b v1.LogicalBackupElasticsearch
			g.Expect(k8s.Get(ctx, client.ObjectKey{Namespace: bareMetal, Name: backupName}, &b)).To(Succeed())
			if b.Status.Phase == v1.LogicalBackupFailed {
				StopTrying(fmt.Sprintf("the backup failed: %v", b.Status.Conditions)).Now()
			}
			g.Expect(b.Status.Phase).To(Equal(v1.LogicalBackupCompleted), "%v", b.Status.Conditions)
		}, backupTimeout, 10*time.Second).Should(Succeed())

		after, err := bucketKeys(bucket)
		Expect(err).NotTo(HaveOccurred())
		written := slices.DeleteFunc(after, func(key string) bool { return slices.Contains(before, key) })
		Expect(written).NotTo(BeEmpty(), "the backup wrote nothing into the bucket %s", bucket)
	})

	// The backup stays until the end: its deletion removes its snapshots,
	// which would empty the bucket that the last spec reads.
	It("keeps the bucket and its backup when the claim is deleted with Orphan", func() {
		before, err := bucketKeys(bucket)
		Expect(err).NotTo(HaveOccurred())

		// Without the optimistic lock, the patch sends no resourceVersion, so
		// the copy of the claim from the first spec does not conflict.
		patch := client.MergeFrom(claim.DeepCopy())
		claim.Spec.DeletionPolicy = pluginsv1alpha1.DeletionPolicyOrphan
		Expect(k8s.Patch(ctx, &claim, patch)).To(Succeed())
		Expect(k8s.Delete(ctx, &claim)).To(Succeed())
		Eventually(func(g Gomega) { expectClaimGone(g, client.ObjectKeyFromObject(&claim)) },
			deletionTimeout, 10*time.Second).Should(Succeed())

		after, err := bucketKeys(bucket)
		Expect(err).NotTo(HaveOccurred())
		Expect(after).To(ContainElements(before))
	})
})

// bareMetalEnvironment returns the Environment of the journey. Each section
// requires its one registration, so the claim gets exactly the plugins of
// the bare-metal set.
func bareMetalEnvironment() *fleetv1alpha1.Environment {
	only := func(registration string) fleetv1alpha1.SectionPolicy {
		return fleetv1alpha1.SectionPolicy{Required: true, Default: registration, Allowed: []string{registration}}
	}

	return &fleetv1alpha1.Environment{
		ObjectMeta: metav1.ObjectMeta{Name: bareMetal},
		Spec: fleetv1alpha1.EnvironmentSpec{
			PlatformConfigRef: fixtureName,
			Presets: fleetv1alpha1.EnvironmentPresets{
				CamundaCluster:       []string{fixtureName},
				ElasticsearchCluster: []string{fixtureName},
			},
			Releases: &fleetv1alpha1.EnvironmentReleases{Allowed: []string{fixtureName}, Default: fixtureName},
			Sections: map[string]fleetv1alpha1.SectionPolicy{
				sectionBackupStorage:    only(registrationRustFS),
				sectionAccess:           only(registrationGateway),
				sectionSecondaryStorage: only(registrationElasticsearch),
			},
			Parameters: map[string]fleetv1alpha1.ParameterPolicies{
				registrationElasticsearch: {
					"preset": {Value: &apiextensionsv1.JSON{Raw: []byte(`"` + fixtureName + `"`)}},
				},
			},
		},
	}
}

func bareMetalClaim() *fleetv1alpha1.CamundaClusterClaim {
	return &fleetv1alpha1.CamundaClusterClaim{
		ObjectMeta: metav1.ObjectMeta{Namespace: bareMetal, Name: claimName},
		Spec: fleetv1alpha1.CamundaClusterClaimSpec{
			PresetRef: fixtureName,
			Plugins: []fleetv1alpha1.PluginSelection{
				{Section: sectionBackupStorage, Registration: registrationRustFS},
				{Section: sectionAccess, Registration: registrationGateway},
				{Section: sectionSecondaryStorage, Registration: registrationElasticsearch},
			},
		},
	}
}

// envoyAddress returns the ClusterIP of the Envoy Service of the Gateway. On
// kind the Service gets no external address, so the journey calls it from
// inside the cluster.
func envoyAddress() (string, error) {
	var services corev1.ServiceList
	selector, err := metav1.ParseToLabelSelector(envoySelector)
	if err != nil {
		return "", err
	}
	matching, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil {
		return "", err
	}
	if err := k8s.List(
		ctx,
		&services,
		client.InNamespace(envoyGatewayNamespace),
		client.MatchingLabelsSelector{Selector: matching},
	); err != nil {
		return "", fmt.Errorf("listing the Envoy Services: %w", err)
	}

	if len(services.Items) == 0 || services.Items[0].Spec.ClusterIP == "" ||
		services.Items[0].Spec.ClusterIP == corev1.ClusterIPNone {
		return "", fmt.Errorf("no Service in %s with a ClusterIP matches %s", envoyGatewayNamespace, envoySelector)
	}

	return services.Items[0].Spec.ClusterIP, nil
}

// operateStatus calls Operate at externalURL with the admin credentials of
// cluster, through the Envoy proxy at address, and returns the HTTP status
// code. The Gateway certificate is self-signed.
func operateStatus(namespace, cluster, externalURL, address string) (string, error) {
	u, err := url.Parse(externalURL)
	if err != nil {
		return "", fmt.Errorf("parsing the external URL %q: %w", externalURL, err)
	}
	port := "443"
	if u.Scheme == "http" {
		port = "80"
	}

	adminSecret := cluster + "-camunda-admin"
	return runPod(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "operate-", Namespace: namespace},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name:    "curl",
			Image:   requiredEnv("CURL_IMAGE"),
			Command: []string{"curl"},
			// The kubelet replaces $(USERNAME) and $(PASSWORD) with the
			// variables of the container.
			Args: []string{
				"-sS", "-k", "-o", "/dev/null", "-w", "%{http_code}",
				"--resolve", u.Hostname() + ":" + port + ":" + address,
				"-u", "$(USERNAME):$(PASSWORD)",
				externalURL + "/operate/",
			},
			Env: []corev1.EnvVar{
				secretEnv("USERNAME", adminSecret, "username"),
				secretEnv("PASSWORD", adminSecret, "password"),
			},
		}}},
	}, podTimeout)
}

// bucketKeys returns the keys of the objects in bucket, read with the admin
// key of RustFS. A bucket that does not exist is an error, never an empty
// list.
func bucketKeys(bucket string) ([]string, error) {
	listing, err := runPod(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "bucket-", Namespace: rustfsNamespace},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name:  "aws",
			Image: requiredEnv("AWS_CLI_IMAGE"),
			Args: []string{
				"s3api", "list-objects-v2", "--bucket", bucket,
				"--query", "Contents[].Key", "--output", "json",
			},
			Env: []corev1.EnvVar{
				secretEnv("AWS_ACCESS_KEY_ID", rustfsAdminSecret, rustfsAccessKeyKey),
				secretEnv("AWS_SECRET_ACCESS_KEY", rustfsAdminSecret, rustfsSecretKeyKey),
				{Name: "AWS_DEFAULT_REGION", Value: "us-east-1"},
				{Name: "AWS_ENDPOINT_URL", Value: rustfsEndpoint},
			},
		}}},
	}, podTimeout)
	if err != nil {
		return nil, err
	}

	return parseKeys(listing)
}

func secretEnv(name, secret, key string) corev1.EnvVar {
	return corev1.EnvVar{
		Name: name,
		ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: secret},
			Key:                  key,
		}},
	}
}
