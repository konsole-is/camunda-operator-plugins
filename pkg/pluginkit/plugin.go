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
	"time"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
)

// Plugin is what a plugin implements. P is the type of the parameters of a
// request and I the type of its inputs, both decoded from JSON. pluginkit
// calls each method with a request whose registration the plugin serves, and
// it owns every status write and every finalizer write.
type Plugin[P, I any] interface {
	// Reconcile brings the resources of the request to their desired state
	// and returns what pluginkit applies and reports. It receives only a
	// request whose parameters pass the schema of its registration. An error
	// sets Ready=False with reason ReconcileError and retries the request.
	Reconcile(ctx context.Context, req Request[P, I]) (Result, error)
	// Delete deletes the resources of a request with the deletion policy
	// Delete that pluginkit does not delete itself: each resource outside the
	// cluster, and each cluster-scoped object, by name. pluginkit deletes the
	// Grants of the request, and the garbage collector deletes the namespaced
	// objects. The finalizer stays until Delete returns nil, so Delete must
	// succeed for resources that are already gone.
	Delete(ctx context.Context, req Request[P, I]) error
	// Orphan releases the resources of a request with the deletion policy
	// Orphan, for example the tags that tie a cloud resource to the request.
	// pluginkit removes the owner references of the Grants and of the
	// namespaced objects of Config.Owns. The finalizer stays until Orphan
	// returns nil.
	Orphan(ctx context.Context, req Request[P, I]) error
}

// Request is one PluginRequest, as pluginkit gives it to a plugin.
type Request[P, I any] struct {
	// Object is a copy of the request. pluginkit ignores changes to it.
	Object *pluginsv1alpha1.PluginRequest
	// Registration is the registration of the request, as the chart gave it.
	Registration *pluginsv1alpha1.PluginRegistration
	// Parameters is spec.parameters, with each template parameter rendered.
	Parameters P
	// Inputs is spec.inputs.
	Inputs I
	// Log carries the key request.
	Log    logr.Logger
	events events.EventRecorder
}

// Event records one event on the request. Call it once for each external
// action, such as the create of a cloud resource, with an action verb such as
// Create or Delete.
func (r Request[P, I]) Event(eventType, reason, action, note string, args ...any) {
	r.events.Eventf(r.Object, nil, eventType, reason, action, note, args...)
}

// Result is what one reconcile of a plugin gives back to pluginkit.
type Result struct {
	// Outputs are the outputs of the request. Each key must be declared in
	// the outputs of the registration.
	Outputs map[string]string
	// Pending, when set, says that the outputs are not ready yet.
	// OutputsReady and Ready are False with its reason, the outputs of the
	// last reconcile stay, and pluginkit applies nothing. Summary, Links,
	// PodSelector and Resources are still written.
	Pending *Waiting
	// NotReady, when set, makes Ready False with its reason, while
	// OutputsReady comes from the applied objects.
	NotReady *Waiting
	// Objects are the Kubernetes objects of the request. pluginkit applies
	// them with server-side apply, labels them, and gives each namespaced one
	// an owner reference to the request. A namespaced object must be in the
	// namespace of the request.
	Objects []client.Object
	// Summary is status.summary. status.summaryTime changes only when the
	// summary changes.
	Summary []pluginsv1alpha1.SummaryItem
	// Links is status.links.
	Links []pluginsv1alpha1.Link
	// PodSelector is status.podSelector.
	PodSelector *metav1.LabelSelector
	// Resources is status.resources.
	Resources []pluginsv1alpha1.ResourceRef
	// RequeueAfter, when not zero, reconciles the request again after it.
	RequeueAfter time.Duration
}

// Waiting is the reason and the message of a False condition.
type Waiting struct {
	Reason  string
	Message string
}

// Config is the setup of a plugin.
type Config struct {
	// Name is the name of the plugin controller: the controller name of each
	// registration, and the name of the event recorder.
	Name string
	// Version is the version of the plugin controller, from its build.
	Version string
	// Registrations are the registrations that the plugin publishes and
	// serves. Their spec.controller and spec.serviceAccount come from Name,
	// Version, and ServiceAccount.
	Registrations []pluginsv1alpha1.PluginRegistration
	// ServiceAccount is the ServiceAccount of the plugin controller.
	ServiceAccount pluginsv1alpha1.ServiceAccountRef
	// Owns holds one object of each kind that the plugin returns in
	// Result.Objects. A change to an object of a namespaced kind reconciles
	// its request again. A request with the deletion policy Orphan releases
	// the objects of the namespaced kinds.
	Owns []client.Object
	// Health checks the plugin, for example its cloud credentials. It sets
	// Ready of each registration. Nil is always healthy.
	Health func(context.Context) error
	// HealthInterval is the time between two health checks. Zero is 30
	// seconds.
	HealthInterval time.Duration
	// Watches reconciles requests again when objects that the plugin does
	// not own change, for example a Secret that a request names. Each kind
	// must be in the scheme of the manager, and the ServiceAccount of the
	// plugin needs list and watch on it. A mapped request of a registration
	// that the plugin does not serve is ignored.
	Watches []Watch
}

// Watch reconciles requests again when an object of one kind changes.
type Watch struct {
	// Object is one object of the kind to watch.
	Object client.Object
	// Requests returns the requests to reconcile for a changed object.
	Requests handler.MapFunc
}
