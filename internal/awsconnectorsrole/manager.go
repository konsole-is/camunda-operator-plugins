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

// Package awsconnectorsrole gives the Connectors of each Camunda cluster an
// AWS IAM role of their own. It serves the connectors-permissions section
// through pluginkit, with a role that the plugin owns for each request, or
// with one role that the admin made.
package awsconnectorsrole

import (
	"cmp"
	"errors"
	"fmt"
	"strings"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"

	"github.com/konsole-is/camunda-operator-plugins/internal/version"
	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

// Options are the settings of the controller that the command line sets.
type Options struct {
	// Name is the controller name. Empty is aws-connectors-role. Controller
	// names are unique in a process, so two controllers in one process need
	// two names.
	Name string
	// Registrations are the registrations that the plugin publishes and
	// serves: RegistrationRole, RegistrationExistingRole, or both.
	Registrations []pluginsv1alpha1.PluginRegistration
	// ServiceAccount is the ServiceAccount of the controller.
	ServiceAccount pluginsv1alpha1.ServiceAccountRef
	// Mode is how the Connectors pods assume their role.
	Mode awskit.Mode
	// RoleNamePrefix starts the name of each role of RegistrationRole. It
	// must differ from the prefix of aws-identity.
	RoleNamePrefix string
	// PermissionsBoundaryARN is the managed policy that bounds each role of
	// RegistrationRole.
	PermissionsBoundaryARN string
	// ExtraTags go on each AWS object, next to the tags of the request.
	ExtraTags awskit.ExtraTags
	// OIDCProviderARN is the IAM OIDC provider of the EKS cluster. ModeIRSA
	// needs it.
	OIDCProviderARN string
	// EKSClusterName is the EKS cluster of the associations. ModePodIdentity
	// needs it.
	EKSClusterName string
	// Region is the AWS region of the EKS cluster. ModePodIdentity needs it.
	Region string
	// Clients are the AWS clients. Setup needs IAM and STS, and EKS with
	// ModePodIdentity.
	Clients awskit.Clients
	// Resync is the pluginkit.Config.Resync of the plugin.
	Resync time.Duration
}

// Setup adds the controller of opts to mgr: its registration publisher and
// the controller that serves its requests. The scheme of mgr must hold the
// Kubernetes kinds and the plugins.camunda.io kinds. It refuses a
// registration that the plugin does not know, an unknown mode, more extra
// tags than awskit.CheckExtraTags allows next to the UID tag and the desired
// tag, ModeIRSA without a valid OIDC provider ARN, ModePodIdentity without an
// EKS cluster name, a region, or an EKS client, and a missing IAM or STS
// client. When opts serves RegistrationRole, it also refuses no permissions
// boundary, and a role name prefix that IAM refuses or that is the default
// prefix of aws-identity.
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
	servesRole := false
	for _, r := range opts.Registrations {
		switch r.Name {
		case RegistrationRole:
			servesRole = true
		case RegistrationExistingRole:
		default:
			return nil, fmt.Errorf(
				"the registration %q is not %s or %s",
				r.Name,
				RegistrationRole,
				RegistrationExistingRole,
			)
		}
	}

	mode, err := awskit.ParseMode(string(opts.Mode))
	if err != nil {
		return nil, fmt.Errorf("mode: %w", err)
	}

	if err := awskit.CheckExtraTags(opts.ExtraTags, ownTags); err != nil {
		return nil, fmt.Errorf("extra tags: %w", err)
	}

	p := &Plugin{mode: mode, extraTags: opts.ExtraTags, clients: opts.Clients}
	switch mode {
	case awskit.ModeIRSA:
		if p.provider, err = awskit.ParseOIDCProvider(opts.OIDCProviderARN); err != nil {
			return nil, fmt.Errorf("the OIDC provider ARN of mode %s: %w", mode, err)
		}
	case awskit.ModePodIdentity:
		if opts.EKSClusterName == "" {
			return nil, fmt.Errorf("the EKS cluster name is empty: mode %s needs it", mode)
		}
		// IAM role names are global in the account, and two regions can each
		// have an EKS cluster of one name.
		if opts.Region == "" {
			return nil, fmt.Errorf("the AWS region is empty: mode %s needs it", mode)
		}
		if opts.Clients.EKS == nil {
			return nil, fmt.Errorf("the EKS client is missing: mode %s needs it", mode)
		}
		p.eksCluster = opts.EKSClusterName
		p.region = opts.Region
	}

	if opts.Clients.IAM == nil || opts.Clients.STS == nil {
		return nil, errors.New("the IAM or the STS client is missing")
	}

	if !servesRole {
		return p, nil
	}

	if opts.PermissionsBoundaryARN == "" {
		return nil, fmt.Errorf(
			"the permissions boundary ARN is empty: every role of %s needs a boundary",
			RegistrationRole,
		)
	}
	if err := awskit.CheckRolePrefix(opts.RoleNamePrefix); err != nil {
		return nil, err
	}
	// IAM role names are unique without regard to case. The options do not
	// hold the prefix of aws-identity, so this checks its default only.
	if strings.EqualFold(opts.RoleNamePrefix, awsIdentityRolePrefix) {
		return nil, fmt.Errorf(
			"role name prefix %q: aws-identity names its roles with it, so both plugins would write one role",
			opts.RoleNamePrefix,
		)
	}
	p.boundary = opts.PermissionsBoundaryARN
	p.prefix = opts.RoleNamePrefix

	return p, nil
}
