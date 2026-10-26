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

package rustfs

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
)

func TestSummary(t *testing.T) {
	const endpoint = "http://rustfs:9000"
	bucket := pluginsv1alpha1.SummaryItem{Label: "Bucket", Value: "b1"}
	at := pluginsv1alpha1.SummaryItem{Label: "Endpoint", Value: endpoint}

	assert.Equal(t, []pluginsv1alpha1.SummaryItem{
		bucket,
		{Label: "Used size", Value: "2Ki"},
		at,
	}, summary("b1", endpoint, 2048, true, nil))

	assert.Equal(t, []pluginsv1alpha1.SummaryItem{
		bucket,
		{Label: "Used size", Value: "Not counted yet", Severity: pluginsv1alpha1.SeverityInfo},
		at,
	}, summary("b1", endpoint, 0, false, nil))

	assert.Equal(t, []pluginsv1alpha1.SummaryItem{
		bucket,
		{Label: "Used size", Value: "Unknown", Severity: pluginsv1alpha1.SeverityWarning},
		at,
	}, summary("b1", endpoint, 0, false, errors.New("connection refused")))
}

func TestLinks(t *testing.T) {
	assert.Empty(t, links(""))
	assert.Equal(
		t,
		[]pluginsv1alpha1.Link{{Name: "RustFS console", URL: "https://console.example.com"}},
		links("https://console.example.com"),
	)
}

func TestResources(t *testing.T) {
	const endpoint = "http://rustfs:9000"
	kept := pluginsv1alpha1.OnOrphanKept
	assert.Equal(t, []pluginsv1alpha1.ResourceRef{
		{Kind: "Bucket", Name: "b1", Location: endpoint, OnOrphan: kept},
		{Kind: "User", Name: "b1", Location: endpoint, OnOrphan: kept},
		{Kind: "Secret", Name: "orders-rustfs", OnOrphan: kept},
		{Kind: "ObjectStorageConfig", Name: "orders", OnOrphan: kept},
	}, resources("b1", endpoint, "orders"))
}
