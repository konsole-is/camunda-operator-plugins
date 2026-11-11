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
	"os"

	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fleetv1alpha1 "github.com/konsole-is/camunda-operator/api/fleet/v1alpha1"
	v1 "github.com/konsole-is/camunda-operator/api/v1"
)

// requiredEnv returns the environment variable name, and fails the spec when
// it is not set.
func requiredEnv(name string) string {
	value := os.Getenv(name)
	Expect(value).NotTo(BeEmpty(), "%s is not set: test/e2e/run.sh sets it", name)

	return value
}

func createAll(objs ...client.Object) {
	for _, obj := range objs {
		Expect(k8s.Create(ctx, obj)).To(Succeed(), "creating %T %s", obj, obj.GetName())
	}
}

// basicPlatform returns a platform config with basic authentication and no
// license, so the cluster runs in non-production mode.
func basicPlatform(name string) *v1.CamundaPlatformConfig {
	return &v1.CamundaPlatformConfig{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       v1.CamundaPlatformConfigSpec{Auth: &v1.PlatformAuthSpec{Method: v1.AuthenticationMethodBasic}},
	}
}

// release returns a release of the Camunda and Elasticsearch versions that
// the operator at the api ref was tested with.
func release(name string) *v1.CamundaRelease {
	return &v1.CamundaRelease{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: v1.CamundaReleaseSpec{
			Version:       requiredEnv("CAMUNDA_VERSION"),
			Elasticsearch: &v1.ReleaseElasticsearchSpec{Version: requiredEnv("ELASTICSEARCH_VERSION")},
		},
	}
}

// clusterPreset returns a cluster of one broker and one gateway that fits a
// kind node beside Elasticsearch, with the sizes of the operator's own e2e
// flows.
func clusterPreset(name string) *v1.CamundaClusterPreset {
	return &v1.CamundaClusterPreset{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: v1.CamundaClusterPresetSpec{Cluster: v1.CamundaClusterSpec{
			Zeebe: &v1.ZeebeSpec{
				WorkloadSpec: v1.WorkloadSpec{Resources: requests("1", "1.5Gi")},
				StorageSize:  new(resource.MustParse("1Gi")),
			},
			Gateway: &v1.GatewaySpec{WorkloadSpec: v1.WorkloadSpec{Resources: requests("500m", "512Mi")}},
		}},
	}
}

// elasticsearchPreset returns one Elasticsearch node that fits a kind node
// beside the cluster. The memory limit holds the heap of 512m and the memory
// outside the heap.
func elasticsearchPreset(name string) *v1.ElasticsearchClusterPreset {
	resources := requests("500m", "1Gi")
	resources.Limits = corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1536Mi")}

	return &v1.ElasticsearchClusterPreset{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: v1.ElasticsearchClusterPresetSpec{Cluster: v1.ElasticsearchClusterSpec{
			Replicas:    new(int32(1)),
			StorageSize: new(resource.MustParse("1Gi")),
			Resources:   resources,
			ExtraEnv:    []corev1.EnvVar{{Name: "ES_JAVA_OPTS", Value: "-Xms512m -Xmx512m"}},
		}},
	}
}

func requests(cpu, memory string) *corev1.ResourceRequirements {
	return &corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse(cpu),
			corev1.ResourceMemory: resource.MustParse(memory),
		},
	}
}

// environmentNamespace returns the namespace name of the Environment
// environment.
func environmentNamespace(name, environment string) *corev1.Namespace {
	return &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{fleetv1alpha1.LabelEnvironment: environment},
		},
	}
}
