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

package awsidentity

import (
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
	v1 "github.com/konsole-is/camunda-operator/api/v1"
)

// readyMessage is the message of Ready=True on a Grant.
const readyMessage = "The role has the statement, and it matches its grant template"

// setGrantConditions sets Applied and MatchesTemplate of grant, and Ready
// from them: True when both are True, else the reason and message of
// Applied when it is False, else those of MatchesTemplate. It leaves the
// other conditions alone and reports whether one of the three changed.
func setGrantConditions(grant *pluginsv1alpha1.Grant, applied, matches cond) bool {
	ready := applied
	switch {
	case applied.status != metav1.ConditionTrue:
	case matches.status != metav1.ConditionTrue:
		ready = matches
	default:
		ready.message = readyMessage
	}

	changed := false
	for condType, c := range map[string]cond{
		pluginsv1alpha1.ConditionApplied:         applied,
		pluginsv1alpha1.ConditionMatchesTemplate: matches,
		v1.ConditionReady:                        ready,
	} {
		if meta.SetStatusCondition(&grant.Status.Conditions, metav1.Condition{
			Type:               condType,
			Status:             c.status,
			Reason:             c.reason,
			Message:            c.message,
			ObservedGeneration: grant.Generation,
		}) {
			changed = true
		}
	}

	return changed
}
