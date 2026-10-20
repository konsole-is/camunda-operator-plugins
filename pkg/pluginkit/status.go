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
	"fmt"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
	v1 "github.com/konsole-is/camunda-operator/api/v1"
)

// readyCondition returns Ready of a request. The first rule that matches
// wins: OutputsReady that is not True gives its reason and message; a Grant
// with Ready=False gives GrantRejected and names the first such Grant by name;
// notReady gives its reason and message; otherwise Ready is True with reason
// Healthy. A Grant without a Ready condition does not count.
func readyCondition(
	outputsReady metav1.Condition,
	grants []pluginsv1alpha1.Grant,
	notReady *Waiting,
	generation int64,
) metav1.Condition {
	ready := metav1.Condition{
		Type:               v1.ConditionReady,
		Status:             metav1.ConditionTrue,
		Reason:             v1.ReasonHealthy,
		ObservedGeneration: generation,
	}

	if outputsReady.Status != metav1.ConditionTrue {
		ready.Status, ready.Reason, ready.Message = metav1.ConditionFalse, outputsReady.Reason, outputsReady.Message
		return ready
	}

	if rejected := firstRejected(grants); rejected != nil {
		cond := meta.FindStatusCondition(rejected.Status.Conditions, v1.ConditionReady)
		ready.Status = metav1.ConditionFalse
		ready.Reason = pluginsv1alpha1.ReasonGrantRejected
		ready.Message = fmt.Sprintf("Grant %s is not ready: %s: %s", rejected.Name, cond.Reason, cond.Message)
		return ready
	}

	if notReady != nil {
		ready.Status, ready.Reason, ready.Message = metav1.ConditionFalse, notReady.Reason, notReady.Message
	}

	return ready
}

func firstRejected(grants []pluginsv1alpha1.Grant) *pluginsv1alpha1.Grant {
	sorted := slices.Clone(grants)
	slices.SortFunc(sorted, func(a, b pluginsv1alpha1.Grant) int { return strings.Compare(a.Name, b.Name) })

	for i := range sorted {
		if meta.IsStatusConditionFalse(sorted[i].Status.Conditions, v1.ConditionReady) {
			return &sorted[i]
		}
	}
	return nil
}
