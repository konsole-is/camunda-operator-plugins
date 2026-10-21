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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

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

	current, err := r.deleteUnwantedGrants(context.Background(), request, []*pluginsv1alpha1.Grant{kept})

	require.NoError(t, err)
	require.Len(t, current, 1)
	assert.Equal(t, "wanted", current[0].Name)
	err = c.Get(context.Background(), client.ObjectKeyFromObject(unowned), &pluginsv1alpha1.Grant{})
	assert.True(t, apierrors.IsNotFound(err), "the Grant without an owner reference is deleted, got %v", err)
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
	grant := func(name string) pluginsv1alpha1.Grant {
		return pluginsv1alpha1.Grant{ObjectMeta: metav1.ObjectMeta{Name: name}}
	}
	grantRow := func(name string) pluginsv1alpha1.ResourceRef {
		return pluginsv1alpha1.ResourceRef{Kind: "Grant", Name: name, OnOrphan: pluginsv1alpha1.OnOrphanKept}
	}

	cases := []struct {
		name   string
		rows   []pluginsv1alpha1.ResourceRef
		grants []pluginsv1alpha1.Grant
		want   []pluginsv1alpha1.ResourceRef
	}{
		{
			name: "no grants keep the rows of the plugin",
			rows: []pluginsv1alpha1.ResourceRef{bucket},
			want: []pluginsv1alpha1.ResourceRef{bucket},
		},
		{
			name:   "each grant gets a row after the rows of the plugin",
			rows:   []pluginsv1alpha1.ResourceRef{bucket},
			grants: []pluginsv1alpha1.Grant{grant("orders-read"), grant("orders-key-use")},
			want:   []pluginsv1alpha1.ResourceRef{bucket, grantRow("orders-read"), grantRow("orders-key-use")},
		},
		{
			name:   "a grant gets a row when the plugin returns no rows",
			grants: []pluginsv1alpha1.Grant{grant("orders-read")},
			want:   []pluginsv1alpha1.ResourceRef{grantRow("orders-read")},
		},
		{
			name: "a grant that the plugin lists gets no second row",
			rows: []pluginsv1alpha1.ResourceRef{
				{Kind: "Grant", Name: "orders-read", Role: "read", OnOrphan: pluginsv1alpha1.OnOrphanKept},
			},
			grants: []pluginsv1alpha1.Grant{grant("orders-read")},
			want: []pluginsv1alpha1.ResourceRef{
				{Kind: "Grant", Name: "orders-read", Role: "read", OnOrphan: pluginsv1alpha1.OnOrphanKept},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, withGrantRows(tc.rows, tc.grants))
		})
	}
}

func TestWithGrantRowsKeepsTheRowsOfThePlugin(t *testing.T) {
	rows := make([]pluginsv1alpha1.ResourceRef, 1, 4)
	rows[0] = pluginsv1alpha1.ResourceRef{Kind: "S3Bucket", Name: "orders", OnOrphan: pluginsv1alpha1.OnOrphanKept}

	withGrantRows(rows, []pluginsv1alpha1.Grant{{ObjectMeta: metav1.ObjectMeta{Name: "orders-read"}}})

	assert.Equal(t, "orders", rows[:2][0].Name)
	assert.Empty(t, rows[:2][1], "the spare capacity of the rows of the plugin stays unwritten")
}
