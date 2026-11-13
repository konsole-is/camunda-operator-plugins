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

// Command aws-s3 runs the AWS S3 plugin. With -version it prints the version
// of the build and exits.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/konsole-is/camunda-operator-plugins/internal/awss3"
	"github.com/konsole-is/camunda-operator-plugins/internal/version"
	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

func main() {
	var opts awss3.Options
	showVersion := flag.Bool("version", false, "Print the version of the build and exit.")
	flag.StringVar(
		&opts.HealthProbeAddress,
		"health-probe-bind-address",
		":8081",
		"The address of /healthz and /readyz.",
	)
	flag.StringVar(
		&opts.MetricsAddress,
		"metrics-bind-address",
		"0",
		"The address of /metrics. 0 turns the metrics off.",
	)
	flag.BoolVar(
		&opts.LeaderElection,
		"leader-elect",
		false,
		"Run the controller only in the replica that holds the leader election Lease.",
	)
	flag.StringVar(
		&opts.LeaderElectionID,
		"leader-election-id",
		"aws-s3.plugins.camunda.io",
		"The name of the leader election Lease, in the namespace of the controller.",
	)
	flag.StringVar(
		&opts.Settings.Region,
		"region",
		"",
		"The AWS region of a bucket whose request sets none. Empty takes the region of the AWS SDK chain.",
	)
	flag.StringVar(
		&opts.Settings.Partition,
		"partition",
		"aws",
		"The AWS partition of the buckets, for their ARNs and their console links.",
	)
	mode := flag.String(
		"mode",
		string(awskit.ModeIRSA),
		"How the pods of a cluster get their role: irsa or podIdentity. Only irsa names the role in the contract.",
	)
	flag.Var(
		&opts.Settings.ExtraTags,
		"extra-tags",
		"Tags, as key=value items separated by commas, for each bucket.",
	)
	flag.DurationVar(
		&opts.Settings.DeleteBudget,
		"delete-budget",
		15*time.Second,
		"The time for which one pass of a delete deletes objects. A larger bucket takes more passes.",
	)
	flag.DurationVar(
		&opts.Resync,
		"resync",
		time.Hour,
		"The time after which the controller reconciles each request again. 0 turns it off.",
	)
	registrations := flag.String(
		"registrations",
		"/etc/plugin/registrations.yaml",
		"The file of the registrations that the plugin publishes and serves.",
	)
	zapOpts := zap.Options{}
	zapOpts.BindFlags(flag.CommandLine)
	flag.Parse()

	if *showVersion {
		fmt.Println(version.Version())
		return
	}

	ctrl.SetLogger(pluginkit.NewLogger(&zapOpts))
	log := ctrl.Log.WithName("setup")

	var err error
	if opts.Settings.Mode, err = awskit.ParseMode(*mode); err != nil {
		log.Error(err, "Could not read -mode")
		os.Exit(1)
	}

	if opts.Registrations, err = pluginkit.LoadRegistrations(*registrations); err != nil {
		log.Error(err, "Could not load the registrations")
		os.Exit(1)
	}

	if opts.ServiceAccount, err = pluginkit.ServiceAccountFromEnv(); err != nil {
		log.Error(err, "Could not find the ServiceAccount of the controller")
		os.Exit(1)
	}

	cfg, err := awskit.LoadConfig(context.Background(), opts.Settings.Region)
	if err != nil {
		log.Error(err, "Could not load the AWS config")
		os.Exit(1)
	}
	if opts.Settings.Region = cfg.Region; cfg.Region == "" {
		log.Error(nil, "Could not find the AWS region: set -region or AWS_REGION")
		os.Exit(1)
	}
	opts.S3 = awskit.S3In(cfg)
	opts.CloudWatch = awskit.CloudWatchIn(cfg)

	mgr, err := awss3.NewManager(ctrl.GetConfigOrDie(), opts)
	if err != nil {
		log.Error(err, "Could not create the manager")
		os.Exit(1)
	}

	log.Info("Starting the manager", "version", version.Version(), "region", opts.Settings.Region)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Error(err, "Manager stopped with an error")
		os.Exit(1)
	}
}
