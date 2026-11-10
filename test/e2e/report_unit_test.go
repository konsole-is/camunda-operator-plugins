//go:build e2e

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

package e2e

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fleetv1alpha1 "github.com/konsole-is/camunda-operator/api/fleet/v1alpha1"
	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
)

func TestDescribeClaimNamesClaimCondition(t *testing.T) {
	claim := testClaim(metav1.Condition{
		Type: "Ready", Status: metav1.ConditionFalse, Reason: "NotAllowed",
		Message: "the deletion policy is Orphan",
	})

	got := describeClaim(claim, nil)
	assert.Contains(t, got, "CamundaClusterClaim bare-metal/orders")
	assert.Contains(t, got, "Ready=False NotAllowed: the deletion policy is Orphan")
}

func TestDescribeClaimNamesRequestCondition(t *testing.T) {
	claim := testClaim()
	requests := []pluginsv1alpha1.PluginRequest{testRequest("orders-access", metav1.Condition{
		Type: "Ready", Status: metav1.ConditionFalse, Reason: "NotAllowedByListeners",
		Message: "route orders-access is not allowed",
	})}

	got := describeClaim(claim, requests)
	assert.Contains(t, got, "PluginRequest orders-access (access, gateway-public)")
	assert.Contains(t, got, "Ready=False NotAllowedByListeners: route orders-access is not allowed")
}

func TestDescribeClaimNamesMissingRequest(t *testing.T) {
	got := describeClaim(testClaim(), nil)
	assert.Contains(t, got, "PluginRequest orders-backup-storage (backup-storage, rustfs-bucket): not found")
}

func TestDescribeClaimLeavesOutTrueConditions(t *testing.T) {
	claim := testClaim(
		metav1.Condition{Type: "RequestsReady", Status: metav1.ConditionTrue, Reason: "Healthy"},
		metav1.Condition{Type: "Ready", Status: metav1.ConditionFalse, Reason: "Pending"},
	)
	requests := []pluginsv1alpha1.PluginRequest{testRequest(
		"orders-access",
		metav1.Condition{Type: "OutputsReady", Status: metav1.ConditionTrue, Reason: "Healthy"},
	)}

	got := describeClaim(claim, requests)
	assert.NotContains(t, got, "RequestsReady")
	assert.NotContains(t, got, "OutputsReady")
	assert.Contains(t, got, "Ready=False Pending")
}

func TestDescribeClaimNamesUnobservedGeneration(t *testing.T) {
	claim := testClaim()
	claim.Generation = 3

	assert.Contains(t, describeClaim(claim, nil), "generation 3 is not observed yet (observed 2)")
}

// testClaim returns the claim bare-metal/orders at generation 2, observed,
// with a backup-storage and an access request and the given conditions.
func testClaim(conditions ...metav1.Condition) *fleetv1alpha1.CamundaClusterClaim {
	return &fleetv1alpha1.CamundaClusterClaim{
		ObjectMeta: metav1.ObjectMeta{Namespace: "bare-metal", Name: "orders", Generation: 2},
		Status: fleetv1alpha1.CamundaClusterClaimStatus{
			ObservedGeneration: 2,
			Conditions:         conditions,
			Requests: []fleetv1alpha1.RequestStatus{
				{Section: "backup-storage", Registration: "rustfs-bucket", Name: "orders-backup-storage"},
				{Section: "access", Registration: "gateway-public", Name: "orders-access"},
			},
		},
	}
}

func testRequest(name string, conditions ...metav1.Condition) pluginsv1alpha1.PluginRequest {
	return pluginsv1alpha1.PluginRequest{
		ObjectMeta: metav1.ObjectMeta{Namespace: "bare-metal", Name: name},
		Status:     pluginsv1alpha1.PluginRequestStatus{Conditions: conditions},
	}
}
