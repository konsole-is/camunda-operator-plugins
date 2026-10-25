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
	"context"
	"os"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
)

// serviceAccount is the ServiceAccount of the controller in the suite.
var serviceAccount = pluginsv1alpha1.ServiceAccountRef{Namespace: "camunda-operator-plugins", Name: "rustfs"}

// suiteSettings are the settings of the RustFS server of the suite.
var suiteSettings = Settings{
	Endpoint:       "http://rustfs.rustfs.svc:9000",
	ConsoleURL:     "https://console.example.com",
	AdminAccessKey: "admin",
	AdminSecretKey: "admin-secret",
}

var (
	ctx        context.Context
	cancel     context.CancelFunc
	testEnv    *envtest.Environment
	restConfig *rest.Config
	mgr        ctrl.Manager
	k8s        client.Client
	fake       *fakeServer
)

func TestRustFS(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "RustFS Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	crds := os.Getenv("ENVTEST_CRDS")
	Expect(crds).NotTo(BeEmpty(), "ENVTEST_CRDS is not set: run make test, which downloads the CRD bases")

	testEnv = &envtest.Environment{CRDDirectoryPaths: []string{crds}, ErrorIfCRDPathMissing: true}
	var err error
	restConfig, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())

	registrations, err := chartRegistrations(GinkgoT().TempDir())
	Expect(err).NotTo(HaveOccurred())

	fake = newFakeServer()
	mgr, err = newManager(restConfig, Options{
		HealthProbeAddress: "0",
		MetricsAddress:     "0",
		Registrations:      registrations,
		ServiceAccount:     serviceAccount,
		Settings:           suiteSettings,
		HealthInterval:     time.Second,
	}, fake, fake)
	Expect(err).NotTo(HaveOccurred())

	k8s, err = client.New(restConfig, client.Options{Scheme: mgr.GetScheme()})
	Expect(err).NotTo(HaveOccurred())

	ctx, cancel = context.WithCancel(context.Background())
	Expect(k8s.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: serviceAccount.Namespace}})).
		To(Succeed())
	Expect(k8s.Create(ctx, backupSection())).To(Succeed())

	names := make([]string, 0, len(registrations))
	for _, r := range registrations {
		names = append(names, r.Name)
	}
	Expect(installRBAC(ctx, k8s, serviceAccount, names)).To(Succeed())

	go func() {
		defer GinkgoRecover()
		Expect(mgr.Start(ctx)).To(Succeed())
	}()
})

var _ = AfterSuite(func() {
	cancel()
	Expect(testEnv.Stop()).To(Succeed())
})
