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
	"bytes"
	"context"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"

	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

// servedFirstNamespace holds a request that exists before the manager starts.
const servedFirstNamespace = "served-first"

var (
	ctx     context.Context
	cancel  context.CancelFunc
	testEnv *envtest.Environment
	k8s     client.Client
	plugin  *testPlugin
	drift   = &driftPlugin{values: map[string]string{}}
	health  = &healthKnob{firstDelay: 2 * time.Second}
	logs    = &syncBuffer{}
)

func TestPluginkit(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "pluginkit Suite")
}

var _ = BeforeSuite(func() {
	SetDefaultEventuallyTimeout(20 * time.Second)
	SetDefaultEventuallyPollingInterval(50 * time.Millisecond)

	crds := os.Getenv("ENVTEST_CRDS")
	Expect(crds).NotTo(BeEmpty(), "ENVTEST_CRDS is not set: run make test, which downloads the CRD bases")

	logger := pluginkit.NewLogger(&zap.Options{DestWriter: io.MultiWriter(GinkgoWriter, logs)})
	logf.SetLogger(logger)

	testEnv = &envtest.Environment{CRDDirectoryPaths: []string{crds}, ErrorIfCRDPathMissing: true}
	cfg, err := testEnv.Start()
	Expect(err).NotTo(HaveOccurred())

	scheme := runtime.NewScheme()
	Expect(clientgoscheme.AddToScheme(scheme)).To(Succeed())
	Expect(pluginsv1alpha1.AddToScheme(scheme)).To(Succeed())

	k8s, err = client.New(cfg, client.Options{Scheme: scheme})
	Expect(err).NotTo(HaveOccurred())

	ctx, cancel = context.WithCancel(context.Background())
	createRequest(servedFirstNamespace)

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 scheme,
		Logger:                 logger,
		HealthProbeBindAddress: "0",
		Metrics:                metricsserver.Options{BindAddress: "0"},
	})
	Expect(err).NotTo(HaveOccurred())

	plugin = &testPlugin{reader: mgr.GetAPIReader(), client: k8s}
	Expect(pluginkit.Setup(mgr, pluginkit.Config{
		Name:           "test-plugin",
		Version:        "1.2.3",
		Registrations:  []pluginsv1alpha1.PluginRegistration{testRegistration()},
		ServiceAccount: pluginsv1alpha1.ServiceAccountRef{Namespace: "plugins", Name: "test-plugin"},
		Owns:           []client.Object{&corev1.ConfigMap{}, &storagev1.StorageClass{}},
		Watches: []pluginkit.Watch{
			{Object: &corev1.Secret{}, Requests: requestsNamingSecret(mgr.GetClient())},
		},
		Health:         health.check,
		HealthInterval: 200 * time.Millisecond,
	}, plugin)).To(Succeed())

	Expect(pluginkit.Setup(mgr, pluginkit.Config{
		Name:           "drift-plugin",
		Version:        "1.2.3",
		Registrations:  []pluginsv1alpha1.PluginRegistration{driftRegistration()},
		ServiceAccount: pluginsv1alpha1.ServiceAccountRef{Namespace: "plugins", Name: "drift-plugin"},
		Resync:         driftResync,
	}, drift)).To(Succeed())

	go func() {
		defer GinkgoRecover()
		Expect(mgr.Start(ctx)).To(Succeed())
	}()
})

var _ = AfterSuite(func() {
	cancel()
	Expect(testEnv.Stop()).To(Succeed())
})

// healthKnob is the health check of the test plugin. Its first check takes
// firstDelay, so that a request is served before the first Ready unless the
// publisher waits for it.
type healthKnob struct {
	mu         sync.Mutex
	err        error
	firstDelay time.Duration
	checked    bool
}

func (h *healthKnob) set(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.err = err
}

func (h *healthKnob) check(ctx context.Context) error {
	h.mu.Lock()
	first := !h.checked
	h.checked = true
	h.mu.Unlock()

	if first {
		select {
		case <-ctx.Done():
		case <-time.After(h.firstDelay):
		}
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	return h.err
}

// syncBuffer is a buffer that the logger and the specs share.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
