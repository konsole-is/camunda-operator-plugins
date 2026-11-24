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

// Package aws runs the AWS controllers of the plugins repository on one
// manager: aws-identity, aws-connectors-role, aws-kms, aws-ebs, and aws-s3.
// Each controller keeps its own controller name and its own registrations.
package aws

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
	v1 "github.com/konsole-is/camunda-operator/api/v1"

	"github.com/konsole-is/camunda-operator-plugins/internal/awsconnectorsrole"
	"github.com/konsole-is/camunda-operator-plugins/internal/awsebs"
	"github.com/konsole-is/camunda-operator-plugins/internal/awsidentity"
	"github.com/konsole-is/camunda-operator-plugins/internal/awskms"
	"github.com/konsole-is/camunda-operator-plugins/internal/awss3"
)

// Options are the settings of the one manager, and the options of each
// controller that runs on it. A controller runs when its field is not nil.
type Options struct {
	// HealthProbeAddress is the address of /healthz and /readyz, for example
	// ":8081". "0" turns the probes off.
	HealthProbeAddress string
	// MetricsAddress is the address of /metrics. "0" turns the metrics off.
	MetricsAddress string
	// LeaderElection makes only the replica that holds the Lease
	// LeaderElectionID run the controllers. It needs a LeaderElectionID.
	LeaderElection   bool
	LeaderElectionID string
	Identity         *awsidentity.Options
	ConnectorsRole   *awsconnectorsrole.Options
	KMS              *awskms.Options
	EBS              *awsebs.Options
	S3               *awss3.Options
}

// NewManager returns one manager for cfg that runs each controller of opts
// that is not nil. It refuses Options without a controller, and Identity and
// a ConnectorsRole that serves awsconnectorsrole.RegistrationRole with role
// name prefixes that are equal without regard to case. It returns the
// refusal of a controller with the controller name in front. /healthz and
// /readyz answer while it runs.
func NewManager(cfg *rest.Config, opts Options) (ctrl.Manager, error) {
	setups := controllers(opts)
	if len(setups) == 0 {
		return nil, errors.New("no controller is on: set at least one of Identity, ConnectorsRole, KMS, EBS, and S3")
	}

	if err := checkRolePrefixes(opts); err != nil {
		return nil, err
	}

	scheme := runtime.NewScheme()
	for _, add := range []struct {
		what    string
		install func(*runtime.Scheme) error
	}{
		{"the Kubernetes kinds", clientgoscheme.AddToScheme},
		{"the plugins.camunda.io kinds", pluginsv1alpha1.AddToScheme},
		{"the core.camunda.io kinds", v1.AddToScheme},
	} {
		if err := add.install(scheme); err != nil {
			return nil, fmt.Errorf("adding %s to the scheme: %w", add.what, err)
		}
	}

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 scheme,
		HealthProbeBindAddress: opts.HealthProbeAddress,
		Metrics:                metricsserver.Options{BindAddress: opts.MetricsAddress},
		LeaderElection:         opts.LeaderElection,
		LeaderElectionID:       opts.LeaderElectionID,
	})
	if err != nil {
		return nil, fmt.Errorf("creating the manager: %w", err)
	}

	for _, s := range setups {
		if err := s.add(mgr); err != nil {
			return nil, fmt.Errorf("setting up %s: %w", s.controller, err)
		}
	}

	if err := mgr.AddHealthzCheck("ping", healthz.Ping); err != nil {
		return nil, fmt.Errorf("adding the health check: %w", err)
	}

	if err := mgr.AddReadyzCheck("ping", healthz.Ping); err != nil {
		return nil, fmt.Errorf("adding the ready check: %w", err)
	}

	return mgr, nil
}

// checkRolePrefixes refuses one role name prefix for Identity and for a
// ConnectorsRole that creates roles.
func checkRolePrefixes(opts Options) error {
	identity, connectors := opts.Identity, opts.ConnectorsRole
	if identity == nil || connectors == nil || !servesRole(connectors.Registrations) {
		return nil
	}

	// IAM role names are unique without regard to case. In IRSA mode both
	// controllers hash the same OIDC issuer, so one prefix gives one role name.
	if strings.EqualFold(identity.RoleNamePrefix, connectors.RoleNamePrefix) {
		return fmt.Errorf(
			"role name prefix %q: aws-identity and aws-connectors-role both use it, "+
				"so in IRSA mode both would write one role for a claim",
			connectors.RoleNamePrefix,
		)
	}

	return nil
}

func servesRole(registrations []pluginsv1alpha1.PluginRegistration) bool {
	return slices.ContainsFunc(registrations, func(r pluginsv1alpha1.PluginRegistration) bool {
		return r.Name == awsconnectorsrole.RegistrationRole
	})
}

// setup adds one controller to a manager.
type setup struct {
	controller string
	add        func(ctrl.Manager) error
}

// controllers returns the setup of each controller that opts turns on, in
// the order aws-identity, aws-connectors-role, aws-kms, aws-ebs, aws-s3.
func controllers(opts Options) []setup {
	var setups []setup
	if o := opts.Identity; o != nil {
		setups = append(setups, setup{"aws-identity", func(m ctrl.Manager) error { return awsidentity.Setup(m, *o) }})
	}
	if o := opts.ConnectorsRole; o != nil {
		setups = append(setups, setup{
			"aws-connectors-role",
			func(m ctrl.Manager) error { return awsconnectorsrole.Setup(m, *o) },
		})
	}
	if o := opts.KMS; o != nil {
		setups = append(setups, setup{"aws-kms", func(m ctrl.Manager) error { return awskms.Setup(m, *o) }})
	}
	if o := opts.EBS; o != nil {
		setups = append(setups, setup{"aws-ebs", func(m ctrl.Manager) error { return awsebs.Setup(m, *o) }})
	}
	if o := opts.S3; o != nil {
		setups = append(setups, setup{"aws-s3", func(m ctrl.Manager) error { return awss3.Setup(m, *o) }})
	}
	return setups
}
