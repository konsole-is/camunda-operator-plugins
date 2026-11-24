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

package awskms

import (
	"cmp"
	"errors"
	"fmt"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"

	"github.com/konsole-is/camunda-operator-plugins/internal/version"
	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

// name is the default controller name of the plugin.
const name = "aws-kms"

// Options are the settings of the controller that the command line sets.
type Options struct {
	// Name is the controller name. Empty is aws-kms. Controller names are
	// unique in a process, so two controllers in one process need two names.
	Name string
	// Registrations are the registrations that the plugin publishes and
	// serves. Setup refuses an empty list.
	Registrations []pluginsv1alpha1.PluginRegistration
	// ServiceAccount is the ServiceAccount of the controller.
	ServiceAccount pluginsv1alpha1.ServiceAccountRef
	// Settings are the settings of the keys.
	Settings Settings
	// KMS returns the KMS client of a region.
	KMS func(region string) awskit.KMS
	// Resync is the pluginkit.Config.Resync of the plugin.
	Resync time.Duration
}

// Setup adds the controller of opts to mgr: its registration publisher and
// the controller that serves its requests. The scheme of mgr must hold the
// Kubernetes kinds and the plugins.camunda.io kinds. It refuses settings that
// fail Settings.Validate and a nil KMS.
func Setup(mgr ctrl.Manager, opts Options) error {
	plugin, err := newPlugin(opts)
	if err != nil {
		return err
	}

	return pluginkit.Setup(mgr, pluginkit.Config{
		Name:           cmp.Or(opts.Name, name),
		Version:        version.Version(),
		Registrations:  opts.Registrations,
		ServiceAccount: opts.ServiceAccount,
		Health:         plugin.health,
		Resync:         opts.Resync,
	}, plugin)
}

// newPlugin returns the plugin of opts.
func newPlugin(opts Options) (*Plugin, error) {
	if err := opts.Settings.Validate(); err != nil {
		return nil, fmt.Errorf("settings: %w", err)
	}

	if opts.KMS == nil {
		return nil, errors.New("the KMS client is missing")
	}

	return &Plugin{keys: keys{settings: opts.Settings, kms: opts.KMS}}, nil
}
