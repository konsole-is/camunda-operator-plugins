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
	"context"
	"encoding/json"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"

	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

const (
	testRegistrationName = "test-plugin"
	testRequestName      = "orders-test"
)

type testParams struct {
	Size   int    `json:"size"`
	Policy string `json:"policy"`
	// Secret names a Secret. The plugin returns its key value as the output
	// input.
	Secret string `json:"secret"`
}

// testPlugin writes one ConfigMap and one StorageClass for each request. Its
// knobs set what it returns.
type testPlugin struct {
	reader client.Reader
	client client.Client

	mu       sync.Mutex
	notReady *pluginkit.Waiting
	pending  *pluginkit.Waiting
	// deleteErr is the error of Delete and of Orphan.
	deleteErr error
	// sawRegistrationFirst is 1 when the registration existed at the first
	// call of Reconcile, and 2 when it did not.
	sawRegistrationFirst int
	// deletedPolicies holds the policy parameter of each request that Delete
	// served, by namespace.
	deletedPolicies map[string]string
	// reconciles counts the calls of Reconcile, by namespace.
	reconciles map[string]int
}

func (p *testPlugin) setNotReady(w *pluginkit.Waiting) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.notReady = w
}

func (p *testPlugin) setPending(w *pluginkit.Waiting) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pending = w
}

func (p *testPlugin) setDeleteErr(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.deleteErr = err
}

func (p *testPlugin) firstSawRegistration() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sawRegistrationFirst
}

func (p *testPlugin) reconcileCount(namespace string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.reconciles[namespace]
}

func (p *testPlugin) Reconcile(
	ctx context.Context,
	req pluginkit.Request[testParams, map[string]string],
) (pluginkit.Result, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.sawRegistrationFirst == 0 {
		p.sawRegistrationFirst = 2
		var registration pluginsv1alpha1.PluginRegistration
		if p.reader.Get(ctx, client.ObjectKey{Name: testRegistrationName}, &registration) == nil {
			p.sawRegistrationFirst = 1
		}
	}

	if p.reconciles == nil {
		p.reconciles = map[string]int{}
	}
	p.reconciles[req.Object.Namespace]++

	req.Log.Info("Served PluginRequest")

	outputs := map[string]string{"configMap": req.Object.Name + "-data"}
	if req.Parameters.Secret != "" {
		var secret corev1.Secret
		key := client.ObjectKey{Namespace: req.Object.Namespace, Name: req.Parameters.Secret}
		err := p.reader.Get(ctx, key, &secret)
		switch {
		case err == nil:
			outputs["input"] = string(secret.Data["value"])
		case !apierrors.IsNotFound(err):
			return pluginkit.Result{}, err
		}
	}

	return pluginkit.Result{
		Outputs:  outputs,
		NotReady: p.notReady,
		Pending:  p.pending,
		Objects: []client.Object{
			&corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: req.Object.Name + "-data"},
				Data:       map[string]string{"policy": req.Parameters.Policy},
			},
			&storagev1.StorageClass{
				ObjectMeta:  metav1.ObjectMeta{Name: storageClassName(req.Object)},
				Provisioner: "example.com/none",
			},
		},
		Summary: []pluginsv1alpha1.SummaryItem{{Label: "Size", Value: "small"}},
	}, nil
}

func (p *testPlugin) deletedPolicy(namespace string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.deletedPolicies[namespace]
}

func (p *testPlugin) Delete(ctx context.Context, req pluginkit.Request[testParams, map[string]string]) error {
	p.mu.Lock()
	err := p.deleteErr
	if err == nil {
		if p.deletedPolicies == nil {
			p.deletedPolicies = map[string]string{}
		}
		p.deletedPolicies[req.Object.Namespace] = req.Parameters.Policy
	}
	p.mu.Unlock()
	if err != nil {
		return err
	}

	sc := &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: storageClassName(req.Object)}}
	if err := client.IgnoreNotFound(p.client.Delete(ctx, sc)); err != nil {
		return err
	}
	req.Event(corev1.EventTypeNormal, "Deleted", "Delete", "Deleted StorageClass %s", sc.Name)
	return nil
}

func (p *testPlugin) Orphan(context.Context, pluginkit.Request[testParams, map[string]string]) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.deleteErr
}

// requestsNamingSecret maps a Secret to the requests in its namespace whose
// parameter secret names it.
func requestsNamingSecret(c client.Reader) handler.MapFunc {
	return func(ctx context.Context, secret client.Object) []reconcile.Request {
		var list pluginsv1alpha1.PluginRequestList
		if err := c.List(ctx, &list, client.InNamespace(secret.GetNamespace())); err != nil {
			return nil
		}

		var requests []reconcile.Request
		for _, request := range list.Items {
			var params testParams
			if request.Spec.Parameters != nil &&
				json.Unmarshal(request.Spec.Parameters.Raw, &params) == nil &&
				params.Secret == secret.GetName() {
				requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&request)})
			}
		}
		return requests
	}
}

func storageClassName(req *pluginsv1alpha1.PluginRequest) string {
	return "pluginkit-test-" + req.Namespace + "-" + req.Name
}

func testRegistration() pluginsv1alpha1.PluginRegistration {
	return pluginsv1alpha1.PluginRegistration{
		ObjectMeta: metav1.ObjectMeta{Name: testRegistrationName},
		Spec: pluginsv1alpha1.PluginRegistrationSpec{
			Sections: []string{"test"},
			Label:    "Test plugin",
			Mode:     pluginsv1alpha1.ModeOptional,
			Parameters: &apiextensionsv1.JSON{Raw: []byte(`{"type":"object","properties":{` +
				`"size":{"type":"integer","minimum":1},"policy":{"type":"string","x-fleet-template":true},` +
				`"secret":{"type":"string"}}}`)},
			Inputs:  []pluginsv1alpha1.InputDecl{{Name: "key", From: "encryption-key/keyRef"}},
			Outputs: []pluginsv1alpha1.OutputDecl{{Key: "configMap"}, {Key: "input"}},
			GrantTemplates: []pluginsv1alpha1.GrantTemplate{
				{
					Name:   "read",
					Format: pluginsv1alpha1.GrantFormatAWSIAMStatement,
					Template: &apiextensionsv1.JSON{
						Raw: []byte(`{"Sid":"{{ .parameters.policy }}","Action":"s3:GetObject",` +
							`"Resource":"{{ .outputs.configMap }}"}`),
					},
				},
				{
					Name:           "key-use",
					Format:         pluginsv1alpha1.GrantFormatAWSIAMStatement,
					Template:       &apiextensionsv1.JSON{Raw: []byte(`{"Action":"kms:Decrypt","Resource":"key"}`)},
					RequiresInputs: []string{"key"},
				},
			},
			DeletionPolicies: []pluginsv1alpha1.DeletionPolicy{
				pluginsv1alpha1.DeletionPolicyDelete,
				pluginsv1alpha1.DeletionPolicyOrphan,
			},
		},
	}
}

// createRequest creates the namespace ns and a request in it for the test
// registration, and returns the request.
func createRequest(ns string, mutate ...func(*pluginsv1alpha1.PluginRequest)) *pluginsv1alpha1.PluginRequest {
	GinkgoHelper()

	Expect(k8s.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})).To(Succeed())

	request := &pluginsv1alpha1.PluginRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testRequestName,
			Namespace: ns,
			Labels: map[string]string{
				pluginsv1alpha1.LabelSection:      "test",
				pluginsv1alpha1.LabelRegistration: testRegistrationName,
				"fleet.camunda.io/claim":          "orders",
			},
		},
		Spec: pluginsv1alpha1.PluginRequestSpec{
			Section:      "test",
			Registration: testRegistrationName,
			ClaimRef:     pluginsv1alpha1.ClaimRef{Kind: pluginsv1alpha1.ClaimKindCamundaClusterClaim, Name: "orders"},
			Target: pluginsv1alpha1.TargetRef{
				Kind:               "CamundaCluster",
				Name:               "orders",
				ServiceAccountName: "orders-camunda",
			},
			Parameters:     &apiextensionsv1.JSON{Raw: []byte(`{"size":1,"policy":"trust {{ .serviceAccount }}"}`)},
			DeletionPolicy: pluginsv1alpha1.DeletionPolicyDelete,
		},
	}
	for _, m := range mutate {
		m(request)
	}

	Expect(k8s.Create(ctx, request)).To(Succeed())
	return request
}

// readRequest returns the current request, or fails the spec when it is gone.
func readRequest(g Gomega, request *pluginsv1alpha1.PluginRequest) *pluginsv1alpha1.PluginRequest {
	var current pluginsv1alpha1.PluginRequest
	g.Expect(k8s.Get(ctx, client.ObjectKeyFromObject(request), &current)).To(Succeed())
	return &current
}

// updateRequest changes the spec or the metadata of request from a fresh read.
func updateRequest(request *pluginsv1alpha1.PluginRequest, change func(*pluginsv1alpha1.PluginRequest)) {
	GinkgoHelper()
	Eventually(func(g Gomega) {
		current := readRequest(g, request)
		change(current)
		g.Expect(k8s.Update(ctx, current)).To(Succeed())
	}).Should(Succeed())
}

// waitReady waits until Ready of request is True.
func waitReady(request *pluginsv1alpha1.PluginRequest) {
	GinkgoHelper()
	Eventually(func(g Gomega) {
		g.Expect(readRequest(g, request).Status.Conditions).To(ContainElement(SatisfyAll(
			HaveField("Type", "Ready"), HaveField("Status", metav1.ConditionTrue),
		)))
	}).Should(Succeed())
}
