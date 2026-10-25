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

// Command rustfs runs the rustfs plugin. With -version it prints the version
// of the build and exits. It reads the admin key of RustFS from the
// environment variables RUSTFS_ADMIN_ACCESS_KEY and RUSTFS_ADMIN_SECRET_KEY.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/konsole-is/camunda-operator-plugins/internal/rustfs"
	"github.com/konsole-is/camunda-operator-plugins/internal/version"
	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

func main() {
	var opts rustfs.Options
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
		"rustfs.plugins.camunda.io",
		"The name of the leader election Lease, in the namespace of the controller.",
	)
	flag.StringVar(
		&opts.Settings.Endpoint,
		"endpoint",
		"",
		"The S3 endpoint of RustFS, as the controller and the Camunda pods reach it.",
	)
	flag.StringVar(&opts.Settings.Region, "region", "us-east-1", "The region of the S3 API of RustFS.")
	flag.StringVar(
		&opts.Settings.ConsoleURL,
		"console-url",
		"",
		"The URL of the RustFS console. Each request links to it when it is set.",
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
	if opts.Registrations, err = pluginkit.LoadRegistrations(*registrations); err != nil {
		log.Error(err, "Could not load the registrations")
		os.Exit(1)
	}

	if opts.ServiceAccount, err = pluginkit.ServiceAccountFromEnv(); err != nil {
		log.Error(err, "Could not find the ServiceAccount of the controller")
		os.Exit(1)
	}

	// The admin key is not a flag, so that it is not in the Pod spec.
	opts.Settings.AdminAccessKey = os.Getenv("RUSTFS_ADMIN_ACCESS_KEY")
	opts.Settings.AdminSecretKey = os.Getenv("RUSTFS_ADMIN_SECRET_KEY")

	mgr, err := rustfs.NewManager(ctrl.GetConfigOrDie(), opts)
	if err != nil {
		log.Error(err, "Could not create the manager")
		os.Exit(1)
	}

	log.Info("Starting the manager", "version", version.Version())
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Error(err, "Manager stopped with an error")
		os.Exit(1)
	}
}
