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

package gatewayaccess_test

import (
	"context"
	"os"
	"sync"
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

	"github.com/konsole-is/camunda-operator-plugins/internal/gatewayaccess"
)

// serviceAccount is the ServiceAccount of the controller in the suite.
var serviceAccount = pluginsv1alpha1.ServiceAccountRef{Namespace: "camunda-operator-plugins", Name: "gateway-access"}

var (
	ctx        context.Context
	cancel     context.CancelFunc
	testEnv    *envtest.Environment
	restConfig *rest.Config
	mgr        ctrl.Manager
	k8s        client.Client
	// refuse holds the names of the routes that the fake Gateway refuses.
	refuse sync.Map
)

func TestGatewayAccess(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Gateway Access Suite")
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

	mgr, err = gatewayaccess.NewManager(restConfig, gatewayaccess.Options{
		HealthProbeAddress:      "0",
		MetricsAddress:          "0",
		Registrations:           registrations,
		ServiceAccount:          serviceAccount,
		ReportCertificateExpiry: true,
	})
	Expect(err).NotTo(HaveOccurred())
	Expect(startFakeGateway(mgr, &refuse)).To(Succeed())

	k8s, err = client.New(restConfig, client.Options{Scheme: mgr.GetScheme()})
	Expect(err).NotTo(HaveOccurred())

	ctx, cancel = context.WithCancel(context.Background())
	for _, ns := range []string{"gateway-system", serviceAccount.Namespace} {
		Expect(k8s.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})).To(Succeed())
	}
	Expect(k8s.Create(ctx, accessSection())).To(Succeed())
	Expect(k8s.Create(ctx, exampleClass())).To(Succeed())
	Expect(k8s.Create(ctx, publicGateway())).To(Succeed())
	secret, err := certificateSecret(time.Now().Add(10 * 24 * time.Hour))
	Expect(err).NotTo(HaveOccurred())
	Expect(k8s.Create(ctx, secret)).To(Succeed())

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
