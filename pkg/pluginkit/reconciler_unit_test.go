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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
)

func TestTemplateDataKeepsLargeIntegers(t *testing.T) {
	request := &pluginsv1alpha1.PluginRequest{
		Spec: pluginsv1alpha1.PluginRequestSpec{Parameters: rawJSON(`{"account":123456789012}`)},
	}

	data, err := templateData(request)
	require.NoError(t, err)
	tmpl, err := ParseTemplate("{{ .parameters.account }}", TemplateGrant)
	require.NoError(t, err)
	got, err := tmpl.Render(data)

	require.NoError(t, err)
	assert.Equal(t, "123456789012", got)
}

func TestDeleteUnwantedGrantsDeletesAGrantWithoutItsOwnerReference(t *testing.T) {
	request := ownedRequest()
	labels := map[string]string{pluginsv1alpha1.LabelRequest: request.Name}
	kept := &pluginsv1alpha1.Grant{ObjectMeta: metav1.ObjectMeta{
		Name: "wanted", Namespace: request.Namespace, Labels: labels,
	}}
	unowned := &pluginsv1alpha1.Grant{ObjectMeta: metav1.ObjectMeta{
		Name: "unwanted", Namespace: request.Namespace, Labels: labels,
	}}
	c := fake.NewClientBuilder().WithScheme(ownedScheme(t)).WithObjects(kept, unowned).Build()
	r := &reconciler[struct{}, struct{}]{client: c, reader: c, events: events.NewFakeRecorder(10)}

	listed := []pluginsv1alpha1.Grant{*kept, *unowned}
	current, err := r.deleteUnwantedGrants(context.Background(), request, listed, []*pluginsv1alpha1.Grant{kept})

	require.NoError(t, err)
	require.Len(t, current, 1)
	assert.Equal(t, "wanted", current[0].Name)
	err = c.Get(context.Background(), client.ObjectKeyFromObject(unowned), &pluginsv1alpha1.Grant{})
	assert.True(t, apierrors.IsNotFound(err), "the Grant without an owner reference is deleted, got %v", err)
}

func TestDeleteUnwantedGrantsReturnsTheListedGrantsWhenADeleteFails(t *testing.T) {
	request := ownedRequest()
	labels := map[string]string{pluginsv1alpha1.LabelRequest: request.Name}
	kept := &pluginsv1alpha1.Grant{ObjectMeta: metav1.ObjectMeta{
		Name: "wanted", Namespace: request.Namespace, Labels: labels,
	}}
	unwanted := &pluginsv1alpha1.Grant{ObjectMeta: metav1.ObjectMeta{
		Name: "unwanted", Namespace: request.Namespace, Labels: labels,
	}}
	c := fake.NewClientBuilder().
		WithScheme(ownedScheme(t)).
		WithObjects(kept, unwanted).
		WithInterceptorFuncs(interceptor.Funcs{
			Delete: func(context.Context, client.WithWatch, client.Object, ...client.DeleteOption) error {
				return errors.New("the API server refused the delete")
			},
		}).
		Build()
	r := &reconciler[struct{}, struct{}]{client: c, reader: c, events: events.NewFakeRecorder(10)}

	listed := []pluginsv1alpha1.Grant{*kept, *unwanted}
	current, err := r.deleteUnwantedGrants(context.Background(), request, listed, []*pluginsv1alpha1.Grant{kept})

	require.Error(t, err)
	names := make([]string, 0, len(current))
	for _, g := range current {
		names = append(names, g.Name)
	}
	assert.ElementsMatch(t, []string{"wanted", "unwanted"}, names)
}

func TestStageGrantsListsTheGrantsThatStillExist(t *testing.T) {
	request := ownedRequest()
	labels := map[string]string{pluginsv1alpha1.LabelRequest: request.Name}
	bucket := pluginsv1alpha1.ResourceRef{Kind: "S3Bucket", Name: "orders", OnOrphan: pluginsv1alpha1.OnOrphanKept}
	request.Status.Resources = []pluginsv1alpha1.ResourceRef{bucket}
	kept := &pluginsv1alpha1.Grant{ObjectMeta: metav1.ObjectMeta{
		Name: "wanted", Namespace: request.Namespace, Labels: labels,
	}}
	unwanted := &pluginsv1alpha1.Grant{ObjectMeta: metav1.ObjectMeta{
		Name: "unwanted", Namespace: request.Namespace, Labels: labels,
	}}
	c := fake.NewClientBuilder().WithScheme(ownedScheme(t)).WithObjects(kept, unwanted).Build()
	r := &reconciler[struct{}, struct{}]{client: c, reader: c, events: events.NewFakeRecorder(10)}
	previous := []pluginsv1alpha1.ResourceRef{{Kind: "Grant", Name: "unwanted", OnOrphan: pluginsv1alpha1.OnOrphanKept}}

	current, err := r.stageGrants(context.Background(), request, previous, []*pluginsv1alpha1.Grant{kept})

	require.NoError(t, err)
	require.Len(t, current, 1)
	assert.Equal(t, []pluginsv1alpha1.ResourceRef{
		bucket,
		{Kind: "Grant", Name: "wanted", OnOrphan: pluginsv1alpha1.OnOrphanKept},
	}, request.Status.Resources)
}

func TestStageGrantsKeepsTheGrantRowsOfTheReadStatusWhenTheListFails(t *testing.T) {
	request := ownedRequest()
	bucket := pluginsv1alpha1.ResourceRef{Kind: "S3Bucket", Name: "orders", OnOrphan: pluginsv1alpha1.OnOrphanKept}
	read := pluginsv1alpha1.ResourceRef{Kind: "Grant", Name: "orders-read", OnOrphan: pluginsv1alpha1.OnOrphanKept}
	request.Status.Resources = []pluginsv1alpha1.ResourceRef{bucket}
	c := fake.NewClientBuilder().
		WithScheme(ownedScheme(t)).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if _, ok := list.(*pluginsv1alpha1.GrantList); ok {
					return errors.New("the API server refused the list")
				}
				return c.List(ctx, list, opts...)
			},
		}).
		Build()
	r := &reconciler[struct{}, struct{}]{client: c, reader: c, events: events.NewFakeRecorder(10)}
	previous := []pluginsv1alpha1.ResourceRef{{Kind: "S3Bucket", Name: "old"}, read}

	_, err := r.stageGrants(context.Background(), request, previous, nil)

	require.Error(t, err)
	assert.Equal(t, []pluginsv1alpha1.ResourceRef{bucket, read}, request.Status.Resources)
}

func TestStageResultKeepsSummaryTimeWhenOnlyTheDefaultSeverityDiffers(t *testing.T) {
	since := metav1.NewTime(metav1.Now().Add(-time.Hour).Truncate(time.Second))
	request := &pluginsv1alpha1.PluginRequest{Status: pluginsv1alpha1.PluginRequestStatus{
		Summary: []pluginsv1alpha1.SummaryItem{
			{Label: "Size", Value: "small", Severity: pluginsv1alpha1.SeverityInfo},
		},
		SummaryTime: &since,
	}}

	stageResult(request, Result{Summary: []pluginsv1alpha1.SummaryItem{{Label: "Size", Value: "small"}}})

	assert.Equal(t, &since, request.Status.SummaryTime)
	assert.Equal(t, pluginsv1alpha1.SeverityInfo, request.Status.Summary[0].Severity)
}

func TestWithGrantRows(t *testing.T) {
	bucket := pluginsv1alpha1.ResourceRef{Kind: "S3Bucket", Name: "orders", OnOrphan: pluginsv1alpha1.OnOrphanKept}
	grantRow := func(name string) pluginsv1alpha1.ResourceRef {
		return pluginsv1alpha1.ResourceRef{Kind: "Grant", Name: name, OnOrphan: pluginsv1alpha1.OnOrphanKept}
	}

	cases := []struct {
		name  string
		rows  []pluginsv1alpha1.ResourceRef
		extra []pluginsv1alpha1.ResourceRef
		want  []pluginsv1alpha1.ResourceRef
	}{
		{
			name: "no grant rows keep the rows of the plugin",
			rows: []pluginsv1alpha1.ResourceRef{bucket},
			want: []pluginsv1alpha1.ResourceRef{bucket},
		},
		{
			name:  "each grant row comes after the rows of the plugin",
			rows:  []pluginsv1alpha1.ResourceRef{bucket},
			extra: []pluginsv1alpha1.ResourceRef{grantRow("orders-read"), grantRow("orders-key-use")},
			want:  []pluginsv1alpha1.ResourceRef{bucket, grantRow("orders-read"), grantRow("orders-key-use")},
		},
		{
			name:  "a grant row is added when the plugin returns no rows",
			extra: []pluginsv1alpha1.ResourceRef{grantRow("orders-read")},
			want:  []pluginsv1alpha1.ResourceRef{grantRow("orders-read")},
		},
		{
			name: "a grant that the plugin lists gets no second row",
			rows: []pluginsv1alpha1.ResourceRef{
				{Kind: "Grant", Name: "orders-read", Role: "read", OnOrphan: pluginsv1alpha1.OnOrphanKept},
			},
			extra: []pluginsv1alpha1.ResourceRef{grantRow("orders-read")},
			want: []pluginsv1alpha1.ResourceRef{
				{Kind: "Grant", Name: "orders-read", Role: "read", OnOrphan: pluginsv1alpha1.OnOrphanKept},
			},
		},
		{
			name:  "the rows of a read status keep their grant rows and drop the others",
			rows:  []pluginsv1alpha1.ResourceRef{bucket},
			extra: []pluginsv1alpha1.ResourceRef{{Kind: "S3Bucket", Name: "old"}, grantRow("orders-read")},
			want:  []pluginsv1alpha1.ResourceRef{bucket, grantRow("orders-read")},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, withGrantRows(tc.rows, tc.extra))
		})
	}
}

func TestWithGrantRowsKeepsTheRowsOfThePlugin(t *testing.T) {
	rows := make([]pluginsv1alpha1.ResourceRef, 1, 4)
	rows[0] = pluginsv1alpha1.ResourceRef{Kind: "S3Bucket", Name: "orders", OnOrphan: pluginsv1alpha1.OnOrphanKept}

	withGrantRows(rows, []pluginsv1alpha1.ResourceRef{{Kind: "Grant", Name: "orders-read"}})

	assert.Equal(t, "orders", rows[:2][0].Name)
	assert.Empty(t, rows[:2][1], "the spare capacity of the rows of the plugin stays unwritten")
}

func TestGrantRows(t *testing.T) {
	grants := []pluginsv1alpha1.Grant{
		{ObjectMeta: metav1.ObjectMeta{Name: "orders-read"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "orders-key-use"}},
	}

	assert.Equal(t, []pluginsv1alpha1.ResourceRef{
		{Kind: "Grant", Name: "orders-read", OnOrphan: pluginsv1alpha1.OnOrphanKept},
		{Kind: "Grant", Name: "orders-key-use", OnOrphan: pluginsv1alpha1.OnOrphanKept},
	}, grantRows(grants))
	assert.Empty(t, grantRows(nil))
}

func TestRequeueAfterResyncsWithinTenPercentAboveResync(t *testing.T) {
	resync := time.Hour

	for range 200 {
		got := requeueAfter(0, resync)

		assert.GreaterOrEqual(t, got, resync)
		assert.LessOrEqual(t, got, resync+resync/10)
	}
}

func TestRequeueAfterSpreadsTheResync(t *testing.T) {
	seen := map[time.Duration]bool{}
	for range 20 {
		seen[requeueAfter(0, time.Hour)] = true
	}

	assert.Greater(t, len(seen), 1, "each requeue after the resync is the same")
}

func TestRequeueAfterKeepsAShorterRequeueOfThePlugin(t *testing.T) {
	assert.Equal(t, 5*time.Minute, requeueAfter(5*time.Minute, time.Hour))
}

func TestRequeueAfterResyncsBeforeALongerRequeueOfThePlugin(t *testing.T) {
	got := requeueAfter(6*time.Hour, time.Hour)

	assert.GreaterOrEqual(t, got, time.Hour)
	assert.LessOrEqual(t, got, time.Hour+6*time.Minute)
}

func TestRequeueAfterWithoutResync(t *testing.T) {
	assert.Zero(t, requeueAfter(0, 0))
	assert.Equal(t, 5*time.Minute, requeueAfter(5*time.Minute, 0))
}

func TestStageResultAddsTheTimeOfACorrectedDrift(t *testing.T) {
	request := &pluginsv1alpha1.PluginRequest{}
	before := time.Now().UTC().Truncate(time.Second)

	stageResult(request, Result{
		Summary:   []pluginsv1alpha1.SummaryItem{{Label: "Size", Value: "small"}},
		Corrected: []string{"created the bucket orders again"},
	})

	require.Len(t, request.Status.Summary, 2)
	item := request.Status.Summary[1]
	assert.Equal(t, "Last drift corrected", item.Label)
	assert.Equal(t, pluginsv1alpha1.SeverityInfo, item.Severity)
	at, err := time.Parse(time.RFC3339, item.Value)
	require.NoError(t, err)
	assert.False(t, at.Before(before), "the item shows the time of this reconcile, got %s", item.Value)
}

func TestStageResultKeepsTheTimeOfTheLastCorrectedDrift(t *testing.T) {
	last := pluginsv1alpha1.SummaryItem{
		Label:    "Last drift corrected",
		Value:    "2026-10-01T08:00:00Z",
		Severity: pluginsv1alpha1.SeverityInfo,
	}
	request := &pluginsv1alpha1.PluginRequest{Status: pluginsv1alpha1.PluginRequestStatus{
		Summary: []pluginsv1alpha1.SummaryItem{{Label: "Size", Value: "small", Severity: pluginsv1alpha1.SeverityInfo}, last},
	}}

	stageResult(request, Result{Summary: []pluginsv1alpha1.SummaryItem{{Label: "Size", Value: "large"}}})

	assert.Equal(t, []pluginsv1alpha1.SummaryItem{
		{Label: "Size", Value: "large", Severity: pluginsv1alpha1.SeverityInfo},
		last,
	}, request.Status.Summary)
}

func TestStageResultAddsNoDriftItemBeforeTheFirstCorrection(t *testing.T) {
	request := &pluginsv1alpha1.PluginRequest{}

	stageResult(request, Result{Summary: []pluginsv1alpha1.SummaryItem{{Label: "Size", Value: "small"}}})

	assert.Equal(t, []pluginsv1alpha1.SummaryItem{
		{Label: "Size", Value: "small", Severity: pluginsv1alpha1.SeverityInfo},
	}, request.Status.Summary)
}
