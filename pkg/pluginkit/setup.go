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
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
)

// Setup adds the plugin to mgr: a Publisher of the registrations of cfg, and
// a controller named cfg.Name that serves each PluginRequest whose
// registration label names one of them. The scheme of mgr must hold the
// plugins.camunda.io kinds and each kind of cfg.Owns and cfg.Watches. Setup
// refuses a cfg without a name or without a registration, a Watch without an
// Object or a Requests function, and a nil plugin.
func Setup[P, I any](mgr ctrl.Manager, cfg Config, plugin Plugin[P, I]) error {
	if cfg.Name == "" {
		return errors.New("pluginkit: the Config needs a Name")
	}

	if len(cfg.Registrations) == 0 {
		return errors.New("pluginkit: the Config needs at least one registration")
	}

	for i, w := range cfg.Watches {
		if w.Object == nil || w.Requests == nil {
			return fmt.Errorf("pluginkit: Watches[%d] needs an Object and a Requests function", i)
		}
	}

	if plugin == nil {
		return errors.New("pluginkit: the plugin must not be nil")
	}

	publisher := newPublisher(mgr.GetClient(), mgr.GetAPIReader(), cfg)
	if err := mgr.Add(publisher); err != nil {
		return fmt.Errorf("adding the registration publisher: %w", err)
	}

	r := &reconciler[P, I]{
		client:        mgr.GetClient(),
		reader:        mgr.GetAPIReader(),
		scheme:        mgr.GetScheme(),
		events:        mgr.GetEventRecorder(cfg.Name),
		cfg:           cfg,
		plugin:        plugin,
		publisher:     publisher,
		registrations: map[string]*pluginsv1alpha1.PluginRegistration{},
	}
	for i := range cfg.Registrations {
		r.registrations[cfg.Registrations[i].Name] = &cfg.Registrations[i]
	}

	b := ctrl.NewControllerManagedBy(mgr).
		Named(cfg.Name).
		For(&pluginsv1alpha1.PluginRequest{}, builder.WithPredicates(registrationLabel(r.registrations))).
		Watches(&pluginsv1alpha1.Grant{}, handler.EnqueueRequestsFromMapFunc(requestOfGrant))
	for _, obj := range cfg.Owns {
		namespaced, err := mgr.GetClient().IsObjectNamespaced(obj)
		if err != nil {
			return fmt.Errorf("finding the scope of %T: %w", obj, err)
		}
		if namespaced {
			b = b.Owns(obj)
			r.namespacedOwns = append(r.namespacedOwns, obj)
		}
	}

	for _, w := range cfg.Watches {
		b = b.Watches(w.Object, handler.EnqueueRequestsFromMapFunc(w.Requests))
	}

	if err := b.Complete(r); err != nil {
		return fmt.Errorf("building the controller %s: %w", cfg.Name, err)
	}

	return nil
}

// registrationLabel passes a PluginRequest whose registration label names one
// of registrations.
func registrationLabel(registrations map[string]*pluginsv1alpha1.PluginRegistration) predicate.Predicate {
	return predicate.NewPredicateFuncs(func(obj client.Object) bool {
		_, ok := registrations[obj.GetLabels()[pluginsv1alpha1.LabelRegistration]]
		return ok
	})
}

// requestOfGrant maps a Grant to the request of its request label, so that a
// Grant without its owner reference still reconciles its request.
func requestOfGrant(_ context.Context, grant client.Object) []reconcile.Request {
	name := grant.GetLabels()[pluginsv1alpha1.LabelRequest]
	if name == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: grant.GetNamespace(), Name: name}}}
}
