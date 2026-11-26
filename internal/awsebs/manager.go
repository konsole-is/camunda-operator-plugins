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

package awsebs

import (
	"cmp"
	"time"

	storagev1 "k8s.io/api/storage/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"

	"github.com/konsole-is/camunda-operator-plugins/internal/version"
	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

// name is the default controller name of the plugin.
const name = "aws-ebs"

// Options are the settings of the controller that the command line sets.
type Options struct {
	// Name is the controller name. Empty is aws-ebs. Controller names are
	// unique in a process, so two controllers in one process need two names.
	Name string
	// Registrations are the registrations that the plugin publishes and
	// serves. Setup refuses an empty list.
	Registrations []pluginsv1alpha1.PluginRegistration
	// ServiceAccount is the ServiceAccount of the controller.
	ServiceAccount pluginsv1alpha1.ServiceAccountRef
	// Resync is the pluginkit.Config.Resync of the plugin. A deleted
	// StorageClass comes back only at a resync, because no change of a
	// cluster-scoped object reconciles its request.
	Resync time.Duration
}

// Setup adds the controller of opts to mgr: its registration publisher and
// the controller that serves its requests. The scheme of mgr must hold the
// Kubernetes kinds and the plugins.camunda.io kinds.
func Setup(mgr ctrl.Manager, opts Options) error {
	plugin := &Plugin{client: mgr.GetClient(), reader: mgr.GetAPIReader()}
	return pluginkit.Setup(mgr, pluginkit.Config{
		Name:           cmp.Or(opts.Name, name),
		Version:        version.Version(),
		Registrations:  opts.Registrations,
		ServiceAccount: opts.ServiceAccount,
		Owns:           []client.Object{&storagev1.StorageClass{}},
		Resync:         opts.Resync,
	}, plugin)
}
