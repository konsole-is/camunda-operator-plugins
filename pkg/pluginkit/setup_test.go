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

package pluginkit

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
)

func TestSetupRefusesAWatchWithoutAnObject(t *testing.T) {
	cfg := watchConfig(Watch{Requests: noRequests})

	err := Setup(unreachableManager(t), cfg, nopPlugin{})

	assert.ErrorContains(t, err, "pluginkit: Watches[1]")
}

func TestSetupRefusesAWatchWithoutRequests(t *testing.T) {
	cfg := watchConfig(Watch{Object: &corev1.Secret{}})

	err := Setup(unreachableManager(t), cfg, nopPlugin{})

	assert.ErrorContains(t, err, "pluginkit: Watches[1]")
}

// watchConfig returns a valid Config whose second Watch is w.
func watchConfig(w Watch) Config {
	return Config{
		Name:          "test-plugin",
		Registrations: []pluginsv1alpha1.PluginRegistration{{}},
		Watches:       []Watch{{Object: &corev1.ConfigMap{}, Requests: noRequests}, w},
	}
}

func noRequests(context.Context, client.Object) []reconcile.Request {
	return nil
}

// unreachableManager returns a manager with no API server behind it.
func unreachableManager(t *testing.T) ctrl.Manager {
	mgr, err := ctrl.NewManager(&rest.Config{Host: "https://127.0.0.1:1"}, ctrl.Options{
		HealthProbeBindAddress: "0",
		Metrics:                metricsserver.Options{BindAddress: "0"},
	})
	require.NoError(t, err)
	return mgr
}

type nopPlugin struct{}

func (nopPlugin) Reconcile(context.Context, Request[struct{}, struct{}]) (Result, error) {
	return Result{}, nil
}

func (nopPlugin) Delete(context.Context, Request[struct{}, struct{}]) error {
	return nil
}

func (nopPlugin) Orphan(context.Context, Request[struct{}, struct{}]) error {
	return nil
}
