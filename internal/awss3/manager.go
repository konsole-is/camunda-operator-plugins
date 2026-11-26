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

package awss3

import (
	"cmp"
	"errors"
	"fmt"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
	v1 "github.com/konsole-is/camunda-operator/api/v1"

	"github.com/konsole-is/camunda-operator-plugins/internal/version"
	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

// name is the default controller name of the plugin.
const name = "aws-s3"

// Options are the settings of the controller that the command line sets.
type Options struct {
	// Name is the controller name. Empty is aws-s3. Controller names are
	// unique in a process, so two controllers in one process need two names.
	Name string
	// Registrations are the registrations that the plugin publishes and
	// serves. Setup refuses an empty list.
	Registrations []pluginsv1alpha1.PluginRegistration
	// ServiceAccount is the ServiceAccount of the controller.
	ServiceAccount pluginsv1alpha1.ServiceAccountRef
	// Settings are the settings of the buckets.
	Settings Settings
	// Resync is the pluginkit.Config.Resync of the plugin.
	Resync time.Duration
	// SettleTime is how long S3 can show a bucket setting as it was before a
	// write of the plugin. Zero is five minutes.
	SettleTime time.Duration
	// S3 returns the S3 client of a region.
	S3 func(region string) awskit.S3
	// CloudWatch returns the CloudWatch client of a region.
	CloudWatch func(region string) awskit.CloudWatch
}

// Setup adds the controller of opts to mgr: its registration publisher and
// the controller that serves its requests. The scheme of mgr must hold the
// Kubernetes kinds, the plugins.camunda.io kinds, and the core.camunda.io
// kinds. It refuses settings that fail Settings.Validate and a nil client
// function.
func Setup(mgr ctrl.Manager, opts Options) error {
	if err := opts.Settings.Validate(); err != nil {
		return fmt.Errorf("settings: %w", err)
	}

	if opts.S3 == nil || opts.CloudWatch == nil {
		return errors.New("the S3 or the CloudWatch client is missing")
	}

	plugin := &Plugin{
		settings: opts.Settings,
		buckets: buckets{
			s3:           opts.S3,
			deleteBudget: opts.Settings.DeleteBudget,
			writes:       newWriteLog(cmp.Or(opts.SettleTime, settleTime), time.Now),
		},
		cloudWatch: opts.CloudWatch,
		reader:     mgr.GetAPIReader(),
		writer:     mgr.GetClient(),
	}

	return pluginkit.Setup(mgr, pluginkit.Config{
		Name:           cmp.Or(opts.Name, name),
		Version:        version.Version(),
		Registrations:  opts.Registrations,
		ServiceAccount: opts.ServiceAccount,
		Owns:           []client.Object{&v1.ObjectStorageConfig{}},
		Health:         plugin.health,
		Resync:         opts.Resync,
	}, plugin)
}
