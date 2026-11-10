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
	"context"
	"os"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fleetv1alpha1 "github.com/konsole-is/camunda-operator/api/fleet/v1alpha1"
	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
	v1 "github.com/konsole-is/camunda-operator/api/v1"
)

// envJourney names the journey to run. Each journey is a Describe with the
// label of its name, and a directory of test/e2e that holds its setup.sh.
const envJourney = "JOURNEY"

// labelBareMetal is the label of the bare-metal journey.
const labelBareMetal = "bare-metal"

var (
	ctx       = context.Background()
	k8s       client.Client
	clientset kubernetes.Interface
)

func TestJourneys(t *testing.T) {
	journey := os.Getenv(envJourney)
	if journey == "" {
		t.Skip("JOURNEY is not set: test/e2e/run.sh runs a journey on a new kind cluster")
	}

	RegisterFailHandler(Fail)
	suiteConfig, reporterConfig := GinkgoConfiguration()
	suiteConfig.LabelFilter = journey
	RunSpecs(t, "Journeys", suiteConfig, reporterConfig)
}

var _ = BeforeSuite(func() {
	scheme := runtime.NewScheme()
	Expect(clientgoscheme.AddToScheme(scheme)).To(Succeed())
	Expect(v1.AddToScheme(scheme)).To(Succeed())
	Expect(fleetv1alpha1.AddToScheme(scheme)).To(Succeed())
	Expect(pluginsv1alpha1.AddToScheme(scheme)).To(Succeed())

	config, err := ctrl.GetConfig()
	Expect(err).NotTo(HaveOccurred())

	k8s, err = client.New(config, client.Options{Scheme: scheme})
	Expect(err).NotTo(HaveOccurred())
	clientset, err = kubernetes.NewForConfig(config)
	Expect(err).NotTo(HaveOccurred())
})
