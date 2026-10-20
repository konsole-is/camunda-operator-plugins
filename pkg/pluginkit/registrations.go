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
	"os"
	"sync/atomic"
	"time"

	"github.com/sourcehawk/operator-component-framework/pkg/component"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/yaml"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
	v1 "github.com/konsole-is/camunda-operator/api/v1"
)

// FieldManager is the server-side apply field manager with which pluginkit
// applies the registrations of a plugin chart.
const FieldManager = "camunda-operator-plugins"

// The defaults of the publisher.
const (
	defaultHealthInterval = 30 * time.Second
	maxRetryDelay         = time.Minute
)

// The environment variables of ServiceAccountFromEnv.
const (
	envPodNamespace       = "POD_NAMESPACE"
	envServiceAccountName = "SERVICE_ACCOUNT_NAME"
)

// LoadRegistrations reads the registrations of a plugin chart from the file
// at path: a YAML list whose items hold a name and the fields of a
// registration spec. An unknown field and an item without a name are errors.
func LoadRegistrations(path string) ([]pluginsv1alpha1.PluginRegistration, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading the registrations: %w", err)
	}

	var items []struct {
		Name string `json:"name"`
		pluginsv1alpha1.PluginRegistrationSpec
	}
	if err := yaml.UnmarshalStrict(raw, &items); err != nil {
		return nil, fmt.Errorf("decoding the registrations of %s: %w", path, err)
	}

	registrations := make([]pluginsv1alpha1.PluginRegistration, 0, len(items))
	for i, item := range items {
		if item.Name == "" {
			return nil, fmt.Errorf("registration %d of %s has no name", i+1, path)
		}
		registrations = append(registrations, pluginsv1alpha1.PluginRegistration{
			ObjectMeta: metav1.ObjectMeta{Name: item.Name},
			Spec:       item.PluginRegistrationSpec,
		})
	}

	return registrations, nil
}

// ServiceAccountFromEnv returns the ServiceAccount of the plugin controller
// from the variables POD_NAMESPACE and SERVICE_ACCOUNT_NAME, which the chart
// sets. A variable that is not set is an error that names it.
func ServiceAccountFromEnv() (pluginsv1alpha1.ServiceAccountRef, error) {
	ref := pluginsv1alpha1.ServiceAccountRef{
		Namespace: os.Getenv(envPodNamespace),
		Name:      os.Getenv(envServiceAccountName),
	}

	var errs []error
	if ref.Namespace == "" {
		errs = append(errs, fmt.Errorf("the variable %s is not set", envPodNamespace))
	}
	if ref.Name == "" {
		errs = append(errs, fmt.Errorf("the variable %s is not set", envServiceAccountName))
	}

	return ref, errors.Join(errs...)
}

// Publisher applies the registrations of a plugin when the manager starts,
// and then keeps their Ready condition current from the health check of the
// plugin. It is a manager Runnable.
type Publisher struct {
	client     client.Client
	reader     client.Reader
	cfg        Config
	retryDelay time.Duration
	published  atomic.Bool
}

func newPublisher(c client.Client, reader client.Reader, cfg Config) *Publisher {
	if cfg.HealthInterval == 0 {
		cfg.HealthInterval = defaultHealthInterval
	}
	return &Publisher{client: c, reader: reader, cfg: cfg, retryDelay: time.Second}
}

// Start applies each registration with server-side apply under FieldManager,
// with the controller and the ServiceAccount of the Config. A failed apply is
// logged and tried again. Then Start sets Ready of each registration from the
// health check, at each health interval, until ctx ends. It never changes the
// Accepted condition, which the operator owns. It returns nil when ctx ends.
func (p *Publisher) Start(ctx context.Context) error {
	log := logf.FromContext(ctx).WithName("publisher")
	ctx = logf.IntoContext(ctx, log)

	if !p.publish(ctx) {
		return nil
	}
	p.published.Store(true)
	log.Info("Applied the PluginRegistrations of the plugin")

	ticker := time.NewTicker(p.cfg.HealthInterval)
	defer ticker.Stop()
	for {
		p.updateReady(ctx)

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// NeedLeaderElection reports true, so that only the leader replica writes the
// registrations.
func (p *Publisher) NeedLeaderElection() bool {
	return true
}

// Published reports whether each registration of the plugin is applied.
func (p *Publisher) Published() bool {
	return p.published.Load()
}

// publish applies the registrations until it succeeds, and reports false when
// ctx ended first.
func (p *Publisher) publish(ctx context.Context) bool {
	log := logf.FromContext(ctx)
	delay := p.retryDelay

	for {
		err := p.apply(ctx)
		if err == nil {
			return true
		}
		log.Error(err, "Failed to apply the PluginRegistrations of the plugin", "retryAfter", delay)

		select {
		case <-ctx.Done():
			return false
		case <-time.After(delay):
		}
		delay = min(2*delay, maxRetryDelay)
	}
}

func (p *Publisher) apply(ctx context.Context) error {
	for _, r := range p.cfg.Registrations {
		registration := &pluginsv1alpha1.PluginRegistration{
			TypeMeta: metav1.TypeMeta{
				APIVersion: pluginsv1alpha1.GroupVersion.String(),
				Kind:       "PluginRegistration",
			},
			ObjectMeta: metav1.ObjectMeta{Name: r.Name},
			Spec:       *r.Spec.DeepCopy(),
		}
		registration.Spec.Controller = pluginsv1alpha1.ControllerInfo{Name: p.cfg.Name, Version: p.cfg.Version}
		registration.Spec.ServiceAccount = p.cfg.ServiceAccount

		//nolint:staticcheck // the repository applies through the deprecated client.Apply patch
		err := p.client.Patch(ctx, registration, client.Apply, client.FieldOwner(FieldManager), client.ForceOwnership)
		if err != nil {
			return fmt.Errorf("applying PluginRegistration %q: %w", r.Name, err)
		}
	}

	return nil
}

// updateReady sets Ready of each registration from one health check. A
// registration whose Ready and observedGeneration already hold the result
// is not written.
func (p *Publisher) updateReady(ctx context.Context) {
	log := logf.FromContext(ctx)

	cond := metav1.Condition{Type: v1.ConditionReady, Status: metav1.ConditionTrue, Reason: v1.ReasonHealthy}
	if p.cfg.Health != nil {
		if err := p.cfg.Health(ctx); err != nil {
			cond = metav1.Condition{
				Type:    v1.ConditionReady,
				Status:  metav1.ConditionFalse,
				Reason:  ReasonUnhealthy,
				Message: err.Error(),
			}
		}
	}

	for _, r := range p.cfg.Registrations {
		var registration pluginsv1alpha1.PluginRegistration
		if err := p.reader.Get(ctx, client.ObjectKey{Name: r.Name}, &registration); err != nil {
			log.Error(err, "Could not read the PluginRegistration", "registration", r.Name)
			continue
		}

		cond.ObservedGeneration = registration.Generation
		changed := meta.SetStatusCondition(&registration.Status.Conditions, cond)
		if !changed && registration.Status.ObservedGeneration == registration.Generation {
			continue
		}
		registration.Status.ObservedGeneration = registration.Generation

		// A conflict means that the operator wrote Accepted after the read.
		// The next health interval writes Ready from a fresh read.
		err := component.FlushStatus(ctx, component.ReconcileContext{Client: p.client, Owner: &registration})
		if err != nil && !apierrors.IsConflict(err) {
			log.Error(err, "Could not write Ready of the PluginRegistration", "registration", r.Name)
		}
	}
}
