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

package skeleton

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

// outputConfigMapName is the output that names the ConfigMap of a request.
const outputConfigMapName = "configMapName"

// Params are the parameters of a skeleton request. The skeleton has none.
type Params struct{}

// Plugin writes one ConfigMap, <request>-skeleton, for each request, and
// publishes its name as the output configMapName.
type Plugin struct{}

// Reconcile returns the ConfigMap of the request and its output.
func (Plugin) Reconcile(_ context.Context, req pluginkit.Request[Params, map[string]string]) (pluginkit.Result, error) {
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: req.Object.Name + "-skeleton"},
		Data:       map[string]string{"request": req.Object.Name},
	}

	return pluginkit.Result{
		Outputs: map[string]string{outputConfigMapName: configMap.Name},
		Objects: []client.Object{configMap},
	}, nil
}

// Delete does nothing more: the ConfigMap is namespaced, so the garbage
// collector deletes it with the request.
func (Plugin) Delete(context.Context, pluginkit.Request[Params, map[string]string]) error {
	return nil
}

// Orphan does nothing more: pluginkit removes the owner reference of the
// ConfigMap.
func (Plugin) Orphan(context.Context, pluginkit.Request[Params, map[string]string]) error {
	return nil
}
