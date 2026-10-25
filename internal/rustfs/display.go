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
	"k8s.io/apimachinery/pkg/api/resource"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
)

// summary returns the summary of the request whose bucket is bucket. sizeKnown
// says whether the RustFS scanner counted the bucket yet, and sizeErr is the
// error of the read of the size.
func summary(bucket, endpoint string, size uint64, sizeKnown bool, sizeErr error) []pluginsv1alpha1.SummaryItem {
	used := pluginsv1alpha1.SummaryItem{Label: "Used size"}
	switch {
	case sizeErr != nil:
		used.Value, used.Severity = "Unknown", pluginsv1alpha1.SeverityWarning
	case !sizeKnown:
		used.Value, used.Severity = "Not counted yet", pluginsv1alpha1.SeverityInfo
	default:
		used.Value = resource.NewQuantity(int64(size), resource.BinarySI).String()
	}

	return []pluginsv1alpha1.SummaryItem{
		{Label: "Bucket", Value: bucket},
		used,
		{Label: "Endpoint", Value: endpoint},
	}
}

// links returns the link to the RustFS console, or none when consoleURL is
// empty.
func links(consoleURL string) []pluginsv1alpha1.Link {
	if consoleURL == "" {
		return nil
	}
	return []pluginsv1alpha1.Link{{Name: "RustFS console", URL: consoleURL}}
}

// resources returns the rows of status.resources of the request. Each one
// stays with the deletion policy Orphan.
func resources(bucket, endpoint, request string) []pluginsv1alpha1.ResourceRef {
	kept := pluginsv1alpha1.OnOrphanKept
	return []pluginsv1alpha1.ResourceRef{
		{Kind: "Bucket", Name: bucket, Location: endpoint, OnOrphan: kept},
		{Kind: "User", Name: bucket, Location: endpoint, OnOrphan: kept},
		{Kind: "Secret", Name: secretName(request), OnOrphan: kept},
		{Kind: "ObjectStorageConfig", Name: request, OnOrphan: kept},
	}
}
