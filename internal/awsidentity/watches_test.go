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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
)

func TestTheGrantWatchPassesSpecAndLabelChangesAndNotStatusWrites(t *testing.T) {
	w := watches(nil)[0]
	_, ok := w.Object.(*pluginsv1alpha1.Grant)
	require.True(t, ok)
	old := &pluginsv1alpha1.Grant{ObjectMeta: metav1.ObjectMeta{
		Generation: 1,
		Labels:     map[string]string{pluginsv1alpha1.LabelRequest: "a"},
	}}
	passes := func(updated *pluginsv1alpha1.Grant) bool {
		for _, p := range w.Predicates {
			if !p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: updated}) {
				return false
			}
		}
		return true
	}

	relabeled := old.DeepCopy()
	relabeled.Labels[pluginsv1alpha1.LabelRequest] = "b"
	respecced := old.DeepCopy()
	respecced.Generation = 2
	statusOnly := old.DeepCopy()
	statusOnly.Status.Conditions = []metav1.Condition{{Type: "Ready"}}

	assert.True(t, passes(relabeled))
	assert.True(t, passes(respecced))
	assert.False(t, passes(statusOnly))
}
