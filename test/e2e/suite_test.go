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
	"errors"
	"fmt"
	"os"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
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
	// restConfig is the config of the kind cluster that TestJourneys
	// checked.
	restConfig *rest.Config
)

func TestJourneys(t *testing.T) {
	journey := os.Getenv(envJourney)
	if journey == "" {
		t.Skip("JOURNEY is not set: test/e2e/run.sh runs a journey on a new kind cluster")
	}

	kubeconfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{},
	)
	raw, err := kubeconfig.RawConfig()
	require.NoError(t, err)
	require.NoError(t, checkKindContext(raw.CurrentContext, os.Getenv("KIND_CLUSTER")))
	restConfig, err = kubeconfig.ClientConfig()
	require.NoError(t, err)

	RegisterFailHandler(Fail)
	suiteConfig, reporterConfig := GinkgoConfiguration()
	suiteConfig.LabelFilter = journey
	suiteConfig.FailOnEmpty = true
	RunSpecs(t, "Journeys", suiteConfig, reporterConfig)
}

// checkKindContext refuses a current kubeconfig context other than the one
// of the kind cluster kindCluster, so that a journey never writes to another
// cluster.
func checkKindContext(current, kindCluster string) error {
	if kindCluster == "" {
		return errors.New("KIND_CLUSTER is not set: test/e2e/run.sh runs a journey on a new kind cluster")
	}
	if want := "kind-" + kindCluster; current != want {
		return fmt.Errorf("the current kubeconfig context is %q, not %q of the kind cluster", current, want)
	}

	return nil
}

func TestCheckKindContext(t *testing.T) {
	require.NoError(t, checkKindContext("kind-journey", "journey"))

	err := checkKindContext("production", "journey")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"production"`)
	assert.Contains(t, err.Error(), `"kind-journey"`)

	assert.Error(t, checkKindContext("kind-", ""))
}

var _ = BeforeSuite(func() {
	scheme := runtime.NewScheme()
	Expect(clientgoscheme.AddToScheme(scheme)).To(Succeed())
	Expect(v1.AddToScheme(scheme)).To(Succeed())
	Expect(fleetv1alpha1.AddToScheme(scheme)).To(Succeed())
	Expect(pluginsv1alpha1.AddToScheme(scheme)).To(Succeed())

	var err error
	k8s, err = client.New(restConfig, client.Options{Scheme: scheme})
	Expect(err).NotTo(HaveOccurred())
	clientset, err = kubernetes.NewForConfig(restConfig)
	Expect(err).NotTo(HaveOccurred())
})
