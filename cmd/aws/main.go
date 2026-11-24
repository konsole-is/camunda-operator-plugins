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

// Command aws runs the AWS controllers of the plugins on one manager. With
// -version it prints the version of the build and exits.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"

	"github.com/konsole-is/camunda-operator-plugins/internal/aws"
	"github.com/konsole-is/camunda-operator-plugins/internal/awskms"
	"github.com/konsole-is/camunda-operator-plugins/internal/version"
	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

// loadConfig loads the AWS config of a region, as awskit.LoadConfig does.
type loadConfig func(ctx context.Context, region string) (awssdk.Config, error)

func main() {
	f := bindFlags(flag.CommandLine)
	zapOpts := zap.Options{}
	zapOpts.BindFlags(flag.CommandLine)
	flag.Parse()

	if f.version {
		fmt.Println(version.Version())
		return
	}

	ctrl.SetLogger(pluginkit.NewLogger(&zapOpts))
	log := ctrl.Log.WithName("setup")

	account, err := pluginkit.ServiceAccountFromEnv()
	if err != nil {
		log.Error(err, "Could not find the ServiceAccount of the controller")
		os.Exit(1)
	}

	opts, err := managerOptions(context.Background(), f, account, awskit.LoadConfig)
	if err != nil {
		log.Error(err, "Could not set up the controllers")
		os.Exit(1)
	}

	mgr, err := aws.NewManager(ctrl.GetConfigOrDie(), opts)
	if err != nil {
		log.Error(err, "Could not create the manager")
		os.Exit(1)
	}

	log.Info("Starting the manager", "version", version.Version(), "controllers", controllerNames(opts))
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Error(err, "Manager stopped with an error")
		os.Exit(1)
	}
}

// managerOptions returns the options of the manager, with a controller for
// each registrations flag that names a file, served by account. It calls
// load, and AWS, only when aws-identity, aws-connectors-role, aws-kms, or
// aws-s3 is on: aws-ebs makes no AWS call. It refuses aws-kms and aws-s3
// without a region.
func managerOptions(
	ctx context.Context,
	f *flags,
	account pluginsv1alpha1.ServiceAccountRef,
	load loadConfig,
) (aws.Options, error) {
	opts := f.manager
	mode, err := awskit.ParseMode(f.mode)
	if err != nil {
		return opts, fmt.Errorf("reading -mode: %w", err)
	}

	if f.identityRegistrations != "" {
		o := f.identity
		if o.Registrations, err = loadRegistrations("aws-identity", f.identityRegistrations); err != nil {
			return opts, err
		}
		o.ServiceAccount, o.Mode, o.ExtraTags = account, mode, f.extraTags
		o.OIDCProviderARN, o.EKSClusterName = f.oidcProviderARN, f.eksClusterName
		o.AllowedActions = splitList(f.identityAllowedActions)
		opts.Identity = &o
	}

	if f.connectorsRoleRegistrations != "" {
		o := f.connectorsRole
		if o.Registrations, err = loadRegistrations("aws-connectors-role", f.connectorsRoleRegistrations); err != nil {
			return opts, err
		}
		o.ServiceAccount, o.Mode, o.ExtraTags = account, mode, f.extraTags
		o.OIDCProviderARN, o.EKSClusterName = f.oidcProviderARN, f.eksClusterName
		opts.ConnectorsRole = &o
	}

	if f.kmsRegistrations != "" {
		o := f.kms
		if o.Registrations, err = loadRegistrations("aws-kms", f.kmsRegistrations); err != nil {
			return opts, err
		}
		o.ServiceAccount, o.Settings.ExtraTags = account, f.extraTags
		o.Settings.KeyAdministratorARNs = splitList(f.kmsAdministrators)
		opts.KMS = &o
	}

	if f.ebsRegistrations != "" {
		o := f.ebs
		if o.Registrations, err = loadRegistrations("aws-ebs", f.ebsRegistrations); err != nil {
			return opts, err
		}
		o.ServiceAccount = account
		opts.EBS = &o
	}

	if f.s3Registrations != "" {
		o := f.s3
		if o.Registrations, err = loadRegistrations("aws-s3", f.s3Registrations); err != nil {
			return opts, err
		}
		o.ServiceAccount, o.Settings.Mode, o.Settings.ExtraTags = account, mode, f.extraTags
		opts.S3 = &o
	}

	if opts.Identity == nil && opts.ConnectorsRole == nil && opts.KMS == nil && opts.S3 == nil {
		return opts, nil
	}

	return opts, addAWS(ctx, &opts, f.region, load)
}

func loadRegistrations(controller, file string) ([]pluginsv1alpha1.PluginRegistration, error) {
	registrations, err := pluginkit.LoadRegistrations(file)
	if err != nil {
		return nil, fmt.Errorf("loading the registrations of %s: %w", controller, err)
	}

	return registrations, nil
}

// splitList returns the items of a comma-separated list, without spaces and
// without empty items.
func splitList(list string) []string {
	var items []string
	for item := range strings.SplitSeq(list, ",") {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}

	return items
}

// addAWS loads the AWS config of region once, and gives each controller of
// opts that calls AWS its clients and the region of the config.
func addAWS(ctx context.Context, opts *aws.Options, region string, load loadConfig) error {
	cfg, err := load(ctx, region)
	if err != nil {
		return err
	}
	clients := awskit.NewClients(cfg)

	if o := opts.Identity; o != nil {
		o.Clients = clients
	}

	if o := opts.ConnectorsRole; o != nil {
		o.Clients, o.Region = clients, cfg.Region
	}

	if (opts.KMS != nil || opts.S3 != nil) && cfg.Region == "" {
		return errors.New("could not find the AWS region: set -region or AWS_REGION")
	}

	if o := opts.KMS; o != nil {
		o.Settings.Region = cfg.Region
		o.Settings.Account, o.Settings.Partition, err = awskms.ResolveAccount(ctx, clients.STS)
		if err != nil {
			return fmt.Errorf("resolving the AWS account of aws-kms: %w", err)
		}
		o.KMS = awskit.KMSIn(cfg)
	}

	if o := opts.S3; o != nil {
		o.Settings.Region = cfg.Region
		o.S3, o.CloudWatch = awskit.S3In(cfg), awskit.CloudWatchIn(cfg)
	}

	return nil
}

// controllerNames returns the names of the controllers that opts turns on.
func controllerNames(opts aws.Options) []string {
	var names []string
	for _, c := range []struct {
		name string
		on   bool
	}{
		{"aws-identity", opts.Identity != nil},
		{"aws-connectors-role", opts.ConnectorsRole != nil},
		{"aws-kms", opts.KMS != nil},
		{"aws-ebs", opts.EBS != nil},
		{"aws-s3", opts.S3 != nil},
	} {
		if c.on {
			names = append(names, c.name)
		}
	}

	return names
}
