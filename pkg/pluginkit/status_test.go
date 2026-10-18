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
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
	v1 "github.com/konsole-is/camunda-operator/api/v1"
)

func grantWithReady(name string, status metav1.ConditionStatus, reason, message string) pluginsv1alpha1.Grant {
	g := pluginsv1alpha1.Grant{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if status != "" {
		g.Status.Conditions = []metav1.Condition{
			{Type: v1.ConditionReady, Status: status, Reason: reason, Message: message},
		}
	}
	return g
}

func TestReadyCondition(t *testing.T) {
	outputsReady := metav1.Condition{Type: "OutputsReady", Status: metav1.ConditionTrue, Reason: "Healthy"}
	outputsWaiting := metav1.Condition{
		Type: "OutputsReady", Status: metav1.ConditionFalse, Reason: "WaitingForBucket", Message: "creating",
	}
	rejected := grantWithReady("read", metav1.ConditionFalse, "OverCeiling", "too wide")
	healthy := grantWithReady("list", metav1.ConditionTrue, "Healthy", "")

	cases := []struct {
		name        string
		outputs     metav1.Condition
		grants      []pluginsv1alpha1.Grant
		notReady    *Waiting
		wantStatus  metav1.ConditionStatus
		wantReason  string
		wantMessage string
	}{
		{
			name: "outputs that are not ready give their reason", outputs: outputsWaiting,
			wantStatus: metav1.ConditionFalse, wantReason: "WaitingForBucket", wantMessage: "creating",
		},
		{
			name:    "a rejected grant leaves the OutputsReady reason when outputs are not ready",
			outputs: outputsWaiting, grants: []pluginsv1alpha1.Grant{rejected},
			wantStatus: metav1.ConditionFalse, wantReason: "WaitingForBucket", wantMessage: "creating",
		},
		{
			name: "a grant with Ready=False gives GrantRejected", outputs: outputsReady,
			grants:     []pluginsv1alpha1.Grant{healthy, rejected},
			wantStatus: metav1.ConditionFalse, wantReason: pluginsv1alpha1.ReasonGrantRejected,
			wantMessage: "Grant read is not ready: OverCeiling: too wide",
		},
		{
			name: "the first rejected grant by name is named", outputs: outputsReady,
			grants: []pluginsv1alpha1.Grant{
				grantWithReady("z", metav1.ConditionFalse, "Rejected", "no"),
				grantWithReady("a", metav1.ConditionFalse, "OverSizeLimit", "big"),
			},
			wantStatus: metav1.ConditionFalse, wantReason: pluginsv1alpha1.ReasonGrantRejected,
			wantMessage: "Grant a is not ready: OverSizeLimit: big",
		},
		{
			name: "a rejected grant wins over the NotReady of the plugin", outputs: outputsReady,
			grants: []pluginsv1alpha1.Grant{rejected}, notReady: &Waiting{Reason: "WaitingForEndpoints"},
			wantStatus: metav1.ConditionFalse, wantReason: pluginsv1alpha1.ReasonGrantRejected,
			wantMessage: "Grant read is not ready: OverCeiling: too wide",
		},
		{
			name: "a grant without a Ready condition does not count", outputs: outputsReady,
			grants:     []pluginsv1alpha1.Grant{grantWithReady("new", "", "", "")},
			wantStatus: metav1.ConditionTrue, wantReason: v1.ReasonHealthy,
		},
		{
			name: "the NotReady of the plugin gives its reason", outputs: outputsReady,
			notReady:   &Waiting{Reason: "WaitingForEndpoints", Message: "waiting"},
			wantStatus: metav1.ConditionFalse, wantReason: "WaitingForEndpoints", wantMessage: "waiting",
		},
		{
			name: "everything ready gives Healthy", outputs: outputsReady, grants: []pluginsv1alpha1.Grant{healthy},
			wantStatus: metav1.ConditionTrue, wantReason: v1.ReasonHealthy,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := readyCondition(tc.outputs, tc.grants, tc.notReady, 4)

			assert.Equal(t, v1.ConditionReady, got.Type)
			assert.Equal(t, tc.wantStatus, got.Status)
			assert.Equal(t, tc.wantReason, got.Reason)
			assert.Equal(t, tc.wantMessage, got.Message)
			assert.Equal(t, int64(4), got.ObservedGeneration)
		})
	}
}
