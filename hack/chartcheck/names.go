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

package main

import (
	"cmp"
	"slices"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// objectNames returns one line "<Kind> <namespace>/<name>" for each object,
// with "-" as the namespace of an object that sets none, and one line
// "Lease <namespace>/<id>" for each Deployment whose controller container
// elects a leader. The Lease is in the namespace of the Deployment, or in
// namespace when the Deployment sets none. A controller without
// -leader-election-id has the id "(default)", the one default of its binary.
// The lines are sorted.
func objectNames(objects []*unstructured.Unstructured, namespace string) []string {
	lines := make([]string, 0, len(objects))
	for _, o := range objects {
		lines = append(lines, o.GetKind()+" "+cmp.Or(o.GetNamespace(), "-")+"/"+o.GetName())

		if o.GetKind() != "Deployment" {
			continue
		}
		var d appsv1.Deployment
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(o.Object, &d); err != nil {
			continue
		}
		for _, c := range d.Spec.Template.Spec.Containers {
			if elect, lease := leaderElection(c.Args); c.Name == controllerContainer && elect {
				lines = append(lines, "Lease "+inNamespace(d.Namespace, namespace)+"/"+cmp.Or(lease, "(default)"))
			}
		}
	}
	slices.Sort(lines)

	return lines
}
