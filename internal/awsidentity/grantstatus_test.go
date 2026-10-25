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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
	v1 "github.com/konsole-is/camunda-operator/api/v1"
)

var (
	healthy = cond{status: metav1.ConditionTrue, reason: v1.ReasonHealthy, message: "ok"}
	over    = cond{status: metav1.ConditionFalse, reason: pluginsv1alpha1.ReasonOverCeiling, message: "m1"}
	changed = cond{status: metav1.ConditionFalse, reason: pluginsv1alpha1.ReasonTemplateChanged, message: "m2"}
)

func TestSetGrantConditionsReadyTakesAppliedFirst(t *testing.T) {
	g := &pluginsv1alpha1.Grant{ObjectMeta: metav1.ObjectMeta{Generation: 3}}

	assert.True(t, setGrantConditions(g, over, changed))

	ready := meta.FindStatusCondition(g.Status.Conditions, v1.ConditionReady)
	require.NotNil(t, ready)
	assert.Equal(t, metav1.ConditionFalse, ready.Status)
	assert.Equal(t, pluginsv1alpha1.ReasonOverCeiling, ready.Reason)
	assert.Equal(t, "m1", ready.Message)
	for _, c := range g.Status.Conditions {
		assert.Equal(t, int64(3), c.ObservedGeneration, c.Type)
	}
	assert.False(t, setGrantConditions(g, over, changed), "the same values change nothing")
}

func TestSetGrantConditionsReadyTakesMatchesTemplateWhenApplied(t *testing.T) {
	g := &pluginsv1alpha1.Grant{}

	setGrantConditions(g, healthy, changed)

	ready := meta.FindStatusCondition(g.Status.Conditions, v1.ConditionReady)
	assert.Equal(t, pluginsv1alpha1.ReasonTemplateChanged, ready.Reason)
	assert.True(t, meta.IsStatusConditionTrue(g.Status.Conditions, pluginsv1alpha1.ConditionApplied))
	assert.True(t, meta.IsStatusConditionFalse(g.Status.Conditions, pluginsv1alpha1.ConditionMatchesTemplate))
}

func TestSetGrantConditionsReadyIsHealthyWhenBothAreTrue(t *testing.T) {
	g := &pluginsv1alpha1.Grant{}

	setGrantConditions(g, healthy, healthy)

	ready := meta.FindStatusCondition(g.Status.Conditions, v1.ConditionReady)
	assert.Equal(t, metav1.ConditionTrue, ready.Status)
	assert.Equal(t, v1.ReasonHealthy, ready.Reason)
}

func TestSetGrantConditionsKeepsTheConditionsOfOtherWriters(t *testing.T) {
	g := &pluginsv1alpha1.Grant{Status: pluginsv1alpha1.GrantStatus{Conditions: []metav1.Condition{
		{Type: "Other", Status: metav1.ConditionTrue, Reason: "X"},
	}}}

	setGrantConditions(g, healthy, healthy)

	assert.NotNil(t, meta.FindStatusCondition(g.Status.Conditions, "Other"))
}
