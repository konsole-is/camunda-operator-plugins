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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/sourcehawk/operator-component-framework/pkg/component"
	"github.com/sourcehawk/operator-component-framework/pkg/primitives/unstructured/static"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
	v1 "github.com/konsole-is/camunda-operator/api/v1"
)

// The component of a request and the requeue of a status conflict.
const (
	outputsComponent = "outputs"
	conflictRequeue  = time.Second
)

// The event and the summary item of a corrected drift.
const (
	eventReasonDriftCorrected = "DriftCorrected"
	eventActionUpdate         = "Update"
	labelDriftCorrected       = "Last drift corrected"
)

// resyncJitter is the largest part of Config.Resync that pluginkit adds to
// it at random.
const resyncJitter = 0.1

// kindGrant is the kind of a status.resources row of a Grant.
const kindGrant = "Grant"

// reconciler serves the PluginRequests of the registrations of one plugin.
type reconciler[P, I any] struct {
	client         client.Client
	reader         client.Reader
	scheme         *runtime.Scheme
	events         events.EventRecorder
	cfg            Config
	plugin         Plugin[P, I]
	publisher      *Publisher
	registrations  map[string]*pluginsv1alpha1.PluginRegistration
	namespacedOwns []client.Object
}

// Reconcile serves one PluginRequest and writes its status once, at the end.
func (r *reconciler[P, I]) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, err error) {
	// The API reader gives the current resourceVersion, so the one status
	// write conflicts only with a write that came after this read.
	request := &pluginsv1alpha1.PluginRequest{}
	if err := r.reader.Get(ctx, req.NamespacedName, request); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// A Grant, an owned object, or a watched object can start a reconcile
	// that the label predicate of the request did not pass.
	registration, ok := r.registrations[request.Spec.Registration]
	if !ok || request.Labels[pluginsv1alpha1.LabelRegistration] != request.Spec.Registration {
		return ctrl.Result{}, nil
	}

	log := logf.FromContext(ctx).WithValues("request", req.String())
	ctx = logf.IntoContext(ctx, log)

	// The contract says that a plugin writes its registration before it
	// serves a request.
	if !r.publisher.Published() {
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}

	rec := component.ReconcileContext{
		Client:        r.client,
		Scheme:        r.scheme,
		EventRecorder: r.events,
		APIReader:     r.reader,
		Owner:         request,
	}
	defer func() {
		result, err = flush(ctx, rec, result, err)
	}()

	if !request.DeletionTimestamp.IsZero() {
		return r.delete(ctx, request, registration)
	}

	return r.serve(ctx, rec, request, registration)
}

// flush writes the status that the reconcile staged. A conflict is a requeue
// with no error, and a request that is gone is not an error.
func flush(ctx context.Context, rec component.ReconcileContext, result ctrl.Result, err error) (ctrl.Result, error) {
	flushErr := component.FlushStatus(ctx, rec)
	switch {
	case flushErr == nil, apierrors.IsNotFound(flushErr):
		return result, err
	case err != nil:
		return result, err
	case apierrors.IsConflict(flushErr):
		return ctrl.Result{RequeueAfter: conflictRequeue}, nil
	default:
		return ctrl.Result{}, flushErr
	}
}

func (r *reconciler[P, I]) serve(
	ctx context.Context,
	rec component.ReconcileContext,
	request *pluginsv1alpha1.PluginRequest,
	registration *pluginsv1alpha1.PluginRegistration,
) (ctrl.Result, error) {
	data, err := templateData(request)
	if err == nil {
		err = ValidateParameters(ctx, registration.Spec.Parameters, request.Spec.Parameters)
	}
	var parameters *apiextensionsv1.JSON
	if err == nil {
		parameters, err = renderParameters(registration.Spec.Parameters, request.Spec.Parameters, data)
	}
	if err == nil {
		// Grant templates read the rendered values, as the plugin does.
		err = setTemplateParameters(&data, parameters)
	}
	if err != nil {
		stage(request, pluginsv1alpha1.ConditionOutputsReady, pluginsv1alpha1.ReasonInvalidParameters, err.Error())
		stage(request, v1.ConditionReady, pluginsv1alpha1.ReasonInvalidParameters, err.Error())
		return ctrl.Result{}, nil
	}

	// A request whose parameters never passed the schema holds no finalizer,
	// so its delete never waits for a cleanup that cannot run.
	if controllerutil.AddFinalizer(request, pluginsv1alpha1.FinalizerCleanup) {
		if err := r.client.Update(ctx, request); err != nil {
			return ctrl.Result{}, fmt.Errorf("adding the finalizer: %w", err)
		}
	}

	pluginRequest, err := r.pluginRequest(request, registration, parameters, logf.FromContext(ctx))
	if err != nil {
		stage(request, v1.ConditionReady, ReasonReconcileError, err.Error())
		return ctrl.Result{}, err
	}

	res, err := r.plugin.Reconcile(ctx, pluginRequest)
	// A retry finds the repaired resources and corrects nothing, so the drift
	// is staged before each step that can fail.
	if len(res.Corrected) > 0 {
		r.events.Eventf(request, nil, corev1.EventTypeNormal, eventReasonDriftCorrected, eventActionUpdate,
			"Corrected drift outside the cluster: %s", strings.Join(res.Corrected, "; "))
		stageDrift(request, time.Now())
	}

	if err == nil {
		err = checkOutputs(registration, res.Outputs)
	}
	if err != nil {
		stage(request, v1.ConditionReady, ReasonReconcileError, err.Error())
		return ctrl.Result{}, err
	}

	if res.Pending != nil {
		current, err := r.grantsOf(ctx, request)
		if err != nil {
			stage(request, v1.ConditionReady, ReasonReconcileError, err.Error())
			return ctrl.Result{}, err
		}

		res.Outputs = request.Status.Outputs
		res.Resources = withGrantRows(res.Resources, grantRows(current))
		stageResult(request, res)
		stage(request, pluginsv1alpha1.ConditionOutputsReady, res.Pending.Reason, res.Pending.Message)
		r.stageReady(request, nil, nil)
		return ctrl.Result{RequeueAfter: requeueAfter(res.RequeueAfter, r.cfg.Resync)}, nil
	}

	data.Outputs = res.Outputs
	grants, err := renderGrants(request, registration, data)
	if err != nil {
		stage(request, v1.ConditionReady, ReasonGrantTemplateInvalid, err.Error())
		return ctrl.Result{}, err
	}

	comp, err := r.outputs(request, res.Objects, grants)
	if err != nil {
		stage(request, v1.ConditionReady, ReasonReconcileError, err.Error())
		return ctrl.Result{}, err
	}

	if err := comp.Reconcile(ctx, rec); err != nil {
		r.stageReady(request, nil, nil)
		return ctrl.Result{}, err
	}

	previous := request.Status.Resources
	stageResult(request, res)

	current, err := r.stageGrants(ctx, request, previous, grants)
	if err != nil {
		stage(request, v1.ConditionReady, ReasonReconcileError, err.Error())
		return ctrl.Result{}, err
	}

	r.stageReady(request, current, res.NotReady)

	return ctrl.Result{RequeueAfter: requeueAfter(res.RequeueAfter, r.cfg.Resync)}, nil
}

// requeueAfter returns the time after which a served request reconciles
// again: requested, the RequeueAfter of the plugin, or a time between resync
// and 10% more when that comes first. Zero means no requeue.
func requeueAfter(requested, resync time.Duration) time.Duration {
	if resync == 0 {
		return requested
	}

	jittered := wait.Jitter(resync, resyncJitter)
	if requested > 0 && requested < jittered {
		return requested
	}
	return jittered
}

// templateData returns the placeholder values of request, without outputs.
func templateData(request *pluginsv1alpha1.PluginRequest) (TemplateData, error) {
	data := TemplateData{
		Namespace:      request.Namespace,
		Claim:          request.Spec.ClaimRef.Name,
		Request:        request.Name,
		ServiceAccount: request.Spec.Target.ServiceAccountName,
		Parameters:     map[string]any{},
	}

	if err := setTemplateParameters(&data, request.Spec.Parameters); err != nil {
		return TemplateData{}, err
	}

	return data, nil
}

// setTemplateParameters sets the parameters of data from parameters. Large
// integers stay exact.
func setTemplateParameters(data *TemplateData, parameters *apiextensionsv1.JSON) error {
	data.Parameters = map[string]any{}
	if parameters == nil || len(parameters.Raw) == 0 {
		return nil
	}

	decoder := json.NewDecoder(bytes.NewReader(parameters.Raw))
	decoder.UseNumber()
	if err := decoder.Decode(&data.Parameters); err != nil {
		return fmt.Errorf("decoding spec.parameters: %w", err)
	}
	return nil
}

// stage sets one condition of request for its current generation.
func stage(request *pluginsv1alpha1.PluginRequest, conditionType, reason, message string) {
	status := metav1.ConditionFalse
	if reason == v1.ReasonHealthy {
		status = metav1.ConditionTrue
	}

	meta.SetStatusCondition(&request.Status.Conditions, metav1.Condition{
		Type:               conditionType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: request.Generation,
	})
	request.SetObservedGeneration(request.Generation)
}

// stageReady sets Ready of request from its staged OutputsReady, grants, and
// notReady.
func (r *reconciler[P, I]) stageReady(
	request *pluginsv1alpha1.PluginRequest,
	grants []pluginsv1alpha1.Grant,
	notReady *Waiting,
) {
	outputs := metav1.Condition{Status: metav1.ConditionUnknown, Reason: string(component.Unknown)}
	if cond := meta.FindStatusCondition(request.Status.Conditions, pluginsv1alpha1.ConditionOutputsReady); cond != nil {
		outputs = *cond
	}

	meta.SetStatusCondition(&request.Status.Conditions, readyCondition(outputs, grants, notReady, request.Generation))
	request.SetObservedGeneration(request.Generation)
}

func (r *reconciler[P, I]) pluginRequest(
	request *pluginsv1alpha1.PluginRequest,
	registration *pluginsv1alpha1.PluginRegistration,
	parameters *apiextensionsv1.JSON,
	log logr.Logger,
) (Request[P, I], error) {
	pluginRequest := Request[P, I]{
		Object:       request.DeepCopy(),
		Registration: registration.DeepCopy(),
		Log:          log,
		events:       r.events,
	}

	if parameters != nil {
		if err := decodeInto(parameters.Raw, &pluginRequest.Parameters); err != nil {
			return Request[P, I]{}, fmt.Errorf("decoding spec.parameters: %w", err)
		}
	}

	inputs, err := json.Marshal(request.Spec.Inputs)
	if err != nil {
		return Request[P, I]{}, fmt.Errorf("encoding spec.inputs: %w", err)
	}
	if err := decodeInto(inputs, &pluginRequest.Inputs); err != nil {
		return Request[P, I]{}, fmt.Errorf("decoding spec.inputs: %w", err)
	}

	return pluginRequest, nil
}

// checkOutputs refuses an output key that the registration does not declare.
func checkOutputs(registration *pluginsv1alpha1.PluginRegistration, outputs map[string]string) error {
	declared := map[string]bool{}
	for _, o := range registration.Spec.Outputs {
		declared[o.Key] = true
	}

	for key := range outputs {
		if !declared[key] {
			return fmt.Errorf("the plugin returned the output %q, which the registration %s does not declare",
				key, registration.Name)
		}
	}

	return nil
}

// outputs returns the component that applies the objects and the grants of
// request, with the condition OutputsReady.
func (r *reconciler[P, I]) outputs(
	request *pluginsv1alpha1.PluginRequest,
	objects []client.Object,
	grants []*pluginsv1alpha1.Grant,
) (*component.Component, error) {
	b := component.NewComponentBuilder().
		WithName(outputsComponent).
		WithConditionType(pluginsv1alpha1.ConditionOutputsReady)

	all := make([]client.Object, 0, len(objects)+len(grants))
	all = append(all, objects...)
	for _, g := range grants {
		all = append(all, g)
	}

	for _, obj := range all {
		namespaced, err := r.client.IsObjectNamespaced(obj)
		if err != nil {
			return nil, fmt.Errorf("finding the scope of %T %q: %w", obj, obj.GetName(), err)
		}

		u, err := prepareOwned(request, obj, namespaced, r.scheme)
		if err != nil {
			return nil, err
		}

		rb := static.NewBuilder(u)
		if !namespaced {
			rb = rb.MarkClusterScoped()
		}
		resource, err := rb.Build()
		if err != nil {
			return nil, fmt.Errorf("building the resource %s %q: %w", u.GetKind(), u.GetName(), err)
		}
		b = b.WithResource(resource)
	}

	return b.Build()
}

// stageGrants deletes each Grant of request that is not in wanted, and adds
// a row for each Grant that still exists to status.resources of request. When
// it cannot list the Grants, it adds the Grant rows of previous instead. It
// returns the Grants that still exist.
func (r *reconciler[P, I]) stageGrants(
	ctx context.Context,
	request *pluginsv1alpha1.PluginRequest,
	previous []pluginsv1alpha1.ResourceRef,
	wanted []*pluginsv1alpha1.Grant,
) ([]pluginsv1alpha1.Grant, error) {
	listed, err := r.grantsOf(ctx, request)
	if err != nil {
		request.Status.Resources = withGrantRows(request.Status.Resources, previous)
		return nil, err
	}

	current, err := r.deleteUnwantedGrants(ctx, request, listed, wanted)
	request.Status.Resources = withGrantRows(request.Status.Resources, grantRows(current))
	return current, err
}

// deleteUnwantedGrants deletes each Grant of listed, the Grants of request,
// that is not in wanted, and returns the Grants of the request that still
// exist, also those that are deleting. With an error, it returns listed.
func (r *reconciler[P, I]) deleteUnwantedGrants(
	ctx context.Context,
	request *pluginsv1alpha1.PluginRequest,
	listed []pluginsv1alpha1.Grant,
	wanted []*pluginsv1alpha1.Grant,
) ([]pluginsv1alpha1.Grant, error) {
	names := map[string]bool{}
	for _, g := range wanted {
		names[g.Name] = true
	}

	for _, g := range listed {
		if names[g.Name] || !g.DeletionTimestamp.IsZero() {
			continue
		}

		if err := r.client.Delete(ctx, &g, client.Preconditions{UID: &g.UID}); client.IgnoreNotFound(err) != nil {
			return listed, fmt.Errorf("deleting Grant %q: %w", g.Name, err)
		}
		r.events.Eventf(request, &g, corev1.EventTypeNormal, eventReasonGrantDeleted, eventActionDelete,
			"Deleted Grant %s, which the request no longer needs", g.Name)
	}

	current, err := r.grantsOf(ctx, request)
	if err != nil {
		return listed, err
	}
	return current, nil
}

// withGrantRows returns rows with each Grant row of extra that rows does not
// list. rows itself does not change.
func withGrantRows(rows, extra []pluginsv1alpha1.ResourceRef) []pluginsv1alpha1.ResourceRef {
	all := slices.Clone(rows)
	for _, e := range extra {
		listed := slices.ContainsFunc(rows, func(r pluginsv1alpha1.ResourceRef) bool {
			return r.Kind == e.Kind && r.Name == e.Name
		})
		if e.Kind == kindGrant && !listed {
			all = append(all, e)
		}
	}
	return all
}

func grantRows(grants []pluginsv1alpha1.Grant) []pluginsv1alpha1.ResourceRef {
	rows := make([]pluginsv1alpha1.ResourceRef, 0, len(grants))
	for _, g := range grants {
		// pluginkit removes only the owner reference of a Grant on Orphan.
		rows = append(rows, pluginsv1alpha1.ResourceRef{
			Kind:     kindGrant,
			Name:     g.Name,
			OnOrphan: pluginsv1alpha1.OnOrphanKept,
		})
	}
	return rows
}

// grantsOf lists the Grants with the request label of request in its
// namespace, from the API server.
func (r *reconciler[P, I]) grantsOf(
	ctx context.Context,
	request *pluginsv1alpha1.PluginRequest,
) ([]pluginsv1alpha1.Grant, error) {
	var list pluginsv1alpha1.GrantList
	if err := r.reader.List(
		ctx,
		&list,
		client.InNamespace(request.Namespace),
		client.MatchingLabels{pluginsv1alpha1.LabelRequest: request.Name},
	); err != nil {
		return nil, fmt.Errorf("listing the Grants of the request: %w", err)
	}
	return list.Items, nil
}

// stageDrift sets the summary item "Last drift corrected" of request, and
// status.summaryTime, to now.
func stageDrift(request *pluginsv1alpha1.PluginRequest, now time.Time) {
	item := pluginsv1alpha1.SummaryItem{
		Label:    labelDriftCorrected,
		Value:    now.UTC().Format(time.RFC3339),
		Severity: pluginsv1alpha1.SeverityInfo,
	}

	summary := slices.DeleteFunc(slices.Clone(request.Status.Summary), func(i pluginsv1alpha1.SummaryItem) bool {
		return i.Label == labelDriftCorrected
	})
	request.Status.Summary = append(summary, item)
	request.Status.SummaryTime = &metav1.Time{Time: now}
}

// stageResult stages the status fields that the plugin returned. The summary
// keeps the item "Last drift corrected" of the staged status.
func stageResult(request *pluginsv1alpha1.PluginRequest, res Result) {
	summary := withDriftItem(res.Summary, request.Status.Summary)

	// The CRD defaults an empty severity to Info, so the stored summary
	// compares equal only with the same default.
	for i := range summary {
		if summary[i].Severity == "" {
			summary[i].Severity = pluginsv1alpha1.SeverityInfo
		}
	}

	if !equality.Semantic.DeepEqual(request.Status.Summary, summary) {
		request.Status.SummaryTime = nil
		if len(summary) > 0 {
			request.Status.SummaryTime = &metav1.Time{Time: time.Now()}
		}
	}

	request.Status.Outputs = res.Outputs
	request.Status.Summary = summary
	request.Status.Links = res.Links
	request.Status.PodSelector = res.PodSelector
	request.Status.Resources = res.Resources
}

// withDriftItem returns a copy of summary with the item "Last drift
// corrected" of previous, when previous has one.
func withDriftItem(summary, previous []pluginsv1alpha1.SummaryItem) []pluginsv1alpha1.SummaryItem {
	all := slices.Clone(summary)
	for _, item := range previous {
		if item.Label == labelDriftCorrected {
			return append(all, item)
		}
	}
	return all
}
