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

package awskms

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"
	corev1 "k8s.io/api/core/v1"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

// The reasons that the plugin sets on a request, beside InvalidParameters.
const (
	// ReasonKeyAliasInUse means that the alias of the request names a KMS key
	// of another request, or a key that an Orphan deletion policy kept. The
	// outputs are not ready.
	ReasonKeyAliasInUse = "KeyAliasInUse"
	// ReasonKeyPendingDeletion means that the key of the request is pending
	// deletion.
	ReasonKeyPendingDeletion = "KeyPendingDeletion"
	// ReasonKeyNotEnabled means that the key of the request is in another
	// state than Enabled, for example Disabled.
	ReasonKeyNotEnabled = "KeyNotEnabled"
	// ReasonReplicasPending means that a replica of the key is not Enabled
	// yet.
	ReasonReplicasPending = "ReplicasPending"
	// ReasonKeyNotMultiRegion means that the request asks for replicas, and
	// its key is a single-Region key.
	ReasonKeyNotMultiRegion = "KeyNotMultiRegion"
)

// The outputs of a request, and the kind of its rows of status.resources.
const (
	outputKeyRef = "keyRef"
	outputFormat = "format"
	formatKMSARN = "aws-kms-arn"
	kindKMSKey   = "KMSKey"
)

const (
	// defaultPendingWindowDays is the deletion window of a request that
	// sets none.
	defaultPendingWindowDays int32 = 30
	// replicaRequeue is the wait before a request reconciles again while a
	// replica is being created.
	replicaRequeue = 5 * time.Second
	// resync is the time after which each request reconciles again, so that
	// drift of a key is repaired.
	resync = 10 * time.Minute
)

// Params are the parameters of a request.
type Params struct {
	// ReplicaRegions are the regions of the replicas of the key. A request
	// that sets them gets a multi-Region key.
	ReplicaRegions []string `json:"replicaRegions,omitempty"`
	// PendingWindowDays is the deletion window of the key when the request
	// is deleted with the deletion policy Delete. Nil is 30.
	PendingWindowDays *int32 `json:"pendingWindowDays,omitempty"`
}

// Plugin serves encryption key requests. Each request is one cluster: the
// plugin keeps one KMS key for its claim, found by the alias of the claim.
type Plugin struct {
	keys keys
}

// Reconcile keeps the key of the request and publishes its ARN. It is
// Pending with the reason KeyAliasInUse when the alias of the claim names a
// key of another request, and with InvalidParameters when replicaRegions
// holds the home region. Ready is False while the key is not Enabled or a
// replica is not Enabled yet, and when replicas are asked for a
// single-Region key. A failed AWS call is an error.
func (p *Plugin) Reconcile(
	ctx context.Context,
	req pluginkit.Request[Params, map[string]string],
) (pluginkit.Result, error) {
	status, actions, err := p.keys.ensure(ctx, refOf(req.Object), req.Parameters.ReplicaRegions)
	corrected := record(req, actions)

	var (
		inUse          aliasInUseError
		invalid        invalidReplicaError
		notMultiRegion notMultiRegionError
	)
	switch {
	case errors.As(err, &inUse):
		return pluginkit.Result{
			Pending:   &pluginkit.Waiting{Reason: ReasonKeyAliasInUse, Message: err.Error()},
			Corrected: corrected,
		}, nil
	case errors.As(err, &invalid):
		return pluginkit.Result{
			Pending: &pluginkit.Waiting{Reason: pluginsv1alpha1.ReasonInvalidParameters, Message: err.Error()},
		}, nil
	case errors.As(err, &notMultiRegion):
	case err != nil:
		return pluginkit.Result{Corrected: corrected}, err
	}

	res, err := result(status, req.Object)
	if err != nil {
		return pluginkit.Result{Corrected: corrected}, err
	}
	if notMultiRegion.KeyARN != "" {
		res.NotReady = &pluginkit.Waiting{Reason: ReasonKeyNotMultiRegion, Message: notMultiRegion.Error()}
	}
	res.Corrected = corrected

	return res, nil
}

func refOf(request *pluginsv1alpha1.PluginRequest) keyRef {
	return keyRef{Namespace: request.Namespace, Claim: request.Spec.ClaimRef.Name, Request: request.Name}
}

// record records one event for each action, and returns the corrections
// among them.
func record(req pluginkit.Request[Params, map[string]string], actions []action) []string {
	var corrected []string
	for _, a := range actions {
		req.Event(corev1.EventTypeNormal, a.Reason, a.Verb, "%s", a.Note)
		if a.Corrected != "" {
			corrected = append(corrected, a.Corrected)
		}
	}
	return corrected
}

// result returns the outputs, the readiness, and the display fields of a
// request whose key has status.
func result(status keyStatus, request *pluginsv1alpha1.PluginRequest) (pluginkit.Result, error) {
	consoleURL, err := awskit.KeyConsoleURL(status.ARN)
	if err != nil {
		return pluginkit.Result{}, err
	}

	res := pluginkit.Result{
		Outputs:   map[string]string{outputKeyRef: status.ARN, outputFormat: formatKMSARN},
		Summary:   summary(status),
		Links:     []pluginsv1alpha1.Link{{Name: "KMS key", URL: consoleURL}},
		Resources: resources(status, request),
	}

	pending := pendingReplica(status.Replicas)
	switch {
	case pendingDeletion(status.State):
		res.NotReady = &pluginkit.Waiting{
			Reason:  ReasonKeyPendingDeletion,
			Message: fmt.Sprintf("The KMS key %s is pending deletion", status.ARN),
		}
	case status.State != types.KeyStateEnabled:
		res.NotReady = &pluginkit.Waiting{
			Reason:  ReasonKeyNotEnabled,
			Message: fmt.Sprintf("The KMS key %s is %s, not Enabled", status.ARN, status.State),
		}
	case pending != "":
		res.NotReady = &pluginkit.Waiting{
			Reason:  ReasonReplicasPending,
			Message: fmt.Sprintf("The replica of the KMS key in %s is not Enabled yet", pending),
		}
		res.RequeueAfter = replicaRequeue
	}

	return res, nil
}

// pendingReplica returns the region of the first replica that is not
// Enabled, or "".
func pendingReplica(replicas []replicaStatus) string {
	for _, r := range replicas {
		if r.State != types.KeyStateEnabled {
			return r.Region
		}
	}
	return ""
}

// summary returns the summary of a key: its state first, then its rotation,
// its alias, its replicas, a Warning for each replica outside
// replicaRegions, and an Error with the date of a pending deletion.
func summary(status keyStatus) []pluginsv1alpha1.SummaryItem {
	items := []pluginsv1alpha1.SummaryItem{{Label: "Key state", Value: string(status.State)}}
	if status.State == types.KeyStateEnabled {
		rotation := "Off"
		if status.Rotation {
			rotation = "On"
		}
		items = append(items, pluginsv1alpha1.SummaryItem{Label: "Rotation", Value: rotation})
	}
	items = append(items, pluginsv1alpha1.SummaryItem{Label: "Alias", Value: status.Alias})

	if len(status.Replicas) > 0 {
		replicas := make([]string, 0, len(status.Replicas))
		for _, r := range status.Replicas {
			replicas = append(replicas, r.Region+" ("+string(r.State)+")")
		}
		items = append(items, pluginsv1alpha1.SummaryItem{Label: "Replicas", Value: strings.Join(replicas, ", ")})
	}

	for _, r := range status.ExtraReplicas {
		items = append(items, pluginsv1alpha1.SummaryItem{
			Label:    "Replica kept",
			Value:    r.Region + " is not in replicaRegions. The replica stays until the request is deleted.",
			Severity: pluginsv1alpha1.SeverityWarning,
		})
	}

	if pendingDeletion(status.State) {
		// KMS gives a primary key its date only after its replicas are gone.
		value := "Pending. AWS deletes the key after its replicas."
		if status.DeletionDate != nil {
			value = "Pending. AWS deletes the key on " + status.DeletionDate.UTC().Format(time.DateOnly) + "."
		}
		items = append(items, pluginsv1alpha1.SummaryItem{
			Label:    "Deletion",
			Value:    value,
			Severity: pluginsv1alpha1.SeverityError,
		})
	}

	return items
}

// resources returns the rows of status.resources: the key, then each
// replica.
func resources(status keyStatus, request *pluginsv1alpha1.PluginRequest) []pluginsv1alpha1.ResourceRef {
	replicas := append(slices.Clone(status.Replicas), status.ExtraReplicas...)
	rows := make([]pluginsv1alpha1.ResourceRef, 0, 1+len(replicas))
	rows = append(rows, pluginsv1alpha1.ResourceRef{
		Kind:     kindKMSKey,
		Name:     status.Alias,
		ID:       status.ARN,
		Location: status.Region,
		Role:     "Encryption key of the cluster " + request.Spec.Target.Name,
		OnOrphan: pluginsv1alpha1.OnOrphanKept,
	})

	for _, r := range replicas {
		rows = append(rows, pluginsv1alpha1.ResourceRef{
			Kind:     kindKMSKey,
			Name:     status.ID,
			ID:       r.ARN,
			Location: r.Region,
			Role:     "Replica of the key",
			OnOrphan: pluginsv1alpha1.OnOrphanKept,
		})
	}

	return rows
}

// Delete schedules the deletion of the key of the request and of its
// replicas after pendingWindowDays, and deletes its alias. A key that is
// gone or pending deletion already is not an error. It refuses a window
// outside 7 to 30 days.
func (p *Plugin) Delete(ctx context.Context, req pluginkit.Request[Params, map[string]string]) error {
	window := defaultPendingWindowDays
	if req.Parameters.PendingWindowDays != nil {
		window = *req.Parameters.PendingWindowDays
	}
	if window < minPendingWindowDays || window > maxPendingWindowDays {
		return fmt.Errorf(
			"pendingWindowDays is %d: KMS allows %d to %d days",
			window,
			minPendingWindowDays,
			maxPendingWindowDays,
		)
	}

	actions, err := p.keys.scheduleDeletion(ctx, refOf(req.Object), window)
	record(req, actions)
	return err
}

// Orphan keeps the key of the request enabled with its alias, and removes the
// request tags from it and from its replicas.
func (p *Plugin) Orphan(ctx context.Context, req pluginkit.Request[Params, map[string]string]) error {
	actions, err := p.keys.release(ctx, refOf(req.Object))
	record(req, actions)
	return err
}

// health checks that the AWS credentials can list the aliases of the home
// region.
func (p *Plugin) health(ctx context.Context) error {
	_, err := p.keys.kms(p.keys.settings.Region).ListAliases(ctx, &kms.ListAliasesInput{Limit: aws.Int32(1)})
	if err != nil {
		return fmt.Errorf("listing the KMS aliases of %s: %w", p.keys.settings.Region, err)
	}
	return nil
}
