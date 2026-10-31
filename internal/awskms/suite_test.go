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

package awskms_test

import (
	"context"
	"os"
	"testing"

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

	"github.com/konsole-is/camunda-operator-plugins/internal/awskms"
	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit/awsfake"
)

var (
	ctx        context.Context
	cancel     context.CancelFunc
	testEnv    *envtest.Environment
	restConfig *rest.Config
	k8s        client.Client
	mgr        ctrl.Manager
	// fake is the KMS of every region of the manager.
	fake *awsfake.KMS
)

func TestAWSKMS(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "AWS KMS Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	crds := os.Getenv("ENVTEST_CRDS")
	Expect(crds).NotTo(BeEmpty(), "ENVTEST_CRDS is not set: run make test, which downloads the CRD bases")

	testEnv = &envtest.Environment{CRDDirectoryPaths: []string{crds}, ErrorIfCRDPathMissing: true}
	var err error
	restConfig, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())

	fake = awsfake.NewKMS()
	sa := pluginsv1alpha1.ServiceAccountRef{Namespace: pluginNamespace, Name: "aws-kms"}
	mgr, err = awskms.NewManager(restConfig, awskms.Options{
		HealthProbeAddress: "0",
		MetricsAddress:     "0",
		Registrations: []pluginsv1alpha1.PluginRegistration{
			kmsRegistration(singleRegion, false),
			kmsRegistration(multiRegion, true),
		},
		ServiceAccount: sa,
		Settings: awskms.Settings{
			Region:      home,
			Partition:   "aws",
			Account:     awsfake.DefaultAccount,
			AliasPrefix: "camunda",
			ExtraTags:   awskit.ExtraTags{"team": "platform"},
		},
		KMS: fake.In,
	})
	Expect(err).NotTo(HaveOccurred())

	k8s, err = client.New(restConfig, client.Options{Scheme: mgr.GetScheme()})
	Expect(err).NotTo(HaveOccurred())

	ctx, cancel = context.WithCancel(context.Background())
	Expect(k8s.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: pluginNamespace}})).To(Succeed())
	Expect(k8s.Create(ctx, encryptionKeySection())).To(Succeed())
	Expect(installRBAC(ctx, k8s, sa, singleRegion, multiRegion)).To(Succeed())
	go func() {
		defer GinkgoRecover()
		Expect(mgr.Start(ctx)).To(Succeed())
	}()
})

var _ = AfterSuite(func() {
	cancel()
	Expect(testEnv.Stop()).To(Succeed())
})
