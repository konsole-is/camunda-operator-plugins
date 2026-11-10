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
	"fmt"
	"slices"
	"strings"

	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fleetv1alpha1 "github.com/konsole-is/camunda-operator/api/fleet/v1alpha1"
	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
	v1 "github.com/konsole-is/camunda-operator/api/v1"
)

// expectClaimReady asserts that the claim key observed its generation, is
// Ready, and that each request it lists exists with OutputsReady and Ready.
// A failure names each condition of the claim and of its requests that is
// not True. It is written for Eventually.
func expectClaimReady(g Gomega, key client.ObjectKey) {
	var claim fleetv1alpha1.CamundaClusterClaim
	g.Expect(k8s.Get(ctx, key, &claim)).To(Succeed())
	requests, err := claimRequests(&claim)
	g.Expect(err).NotTo(HaveOccurred())
	report := describeClaim(&claim, requests)

	g.Expect(claim.Status.ObservedGeneration).To(Equal(claim.Generation), "%s", report)
	g.Expect(meta.IsStatusConditionTrue(claim.Status.Conditions, v1.ConditionReady)).To(BeTrue(), "%s", report)
	g.Expect(requests).To(HaveLen(len(claim.Status.Requests)), "%s", report)
	for _, request := range requests {
		for _, condition := range []string{pluginsv1alpha1.ConditionOutputsReady, v1.ConditionReady} {
			g.Expect(meta.IsStatusConditionTrue(request.Status.Conditions, condition)).To(BeTrue(), "%s", report)
		}
	}
}

// claimRequests returns the PluginRequests that the status of claim lists.
// A listed request that does not exist is left out.
func claimRequests(claim *fleetv1alpha1.CamundaClusterClaim) ([]pluginsv1alpha1.PluginRequest, error) {
	var requests []pluginsv1alpha1.PluginRequest
	for _, listed := range claim.Status.Requests {
		var request pluginsv1alpha1.PluginRequest
		err := k8s.Get(ctx, client.ObjectKey{Namespace: claim.Namespace, Name: listed.Name}, &request)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("reading the PluginRequest %s/%s: %w", claim.Namespace, listed.Name, err)
		}
		requests = append(requests, request)
	}

	return requests, nil
}

// describeClaim returns a report of claim for a failed wait: a generation
// that the claim did not observe yet, each condition of the claim that is
// not True, and for each request in the status of the claim either its
// conditions that are not True, or that it is not in requests.
func describeClaim(claim *fleetv1alpha1.CamundaClusterClaim, requests []pluginsv1alpha1.PluginRequest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "CamundaClusterClaim %s/%s\n", claim.Namespace, claim.Name)
	if claim.Status.ObservedGeneration != claim.Generation {
		fmt.Fprintf(
			&b,
			"  generation %d is not observed yet (observed %d)\n",
			claim.Generation,
			claim.Status.ObservedGeneration,
		)
	}
	writeConditions(&b, claim.Status.Conditions)

	for _, listed := range claim.Status.Requests {
		fmt.Fprintf(&b, "PluginRequest %s (%s, %s)", listed.Name, listed.Section, listed.Registration)
		i := slices.IndexFunc(requests, func(r pluginsv1alpha1.PluginRequest) bool { return r.Name == listed.Name })
		if i == -1 {
			b.WriteString(": not found\n")
			continue
		}
		b.WriteString("\n")
		writeConditions(&b, requests[i].Status.Conditions)
	}

	return b.String()
}

func writeConditions(b *strings.Builder, conditions []metav1.Condition) {
	for _, c := range conditions {
		if c.Status != metav1.ConditionTrue {
			fmt.Fprintf(b, "  %s=%s %s: %s\n", c.Type, c.Status, c.Reason, c.Message)
		}
	}
}

// requestOf returns the request of section that the status of claim lists,
// or nil when the claim lists none or the request is not in requests.
func requestOf(
	requests []pluginsv1alpha1.PluginRequest,
	claim *fleetv1alpha1.CamundaClusterClaim,
	section string,
) *pluginsv1alpha1.PluginRequest {
	i := slices.IndexFunc(
		claim.Status.Requests,
		func(r fleetv1alpha1.RequestStatus) bool { return r.Section == section },
	)
	if i == -1 {
		return nil
	}

	j := slices.IndexFunc(requests, func(r pluginsv1alpha1.PluginRequest) bool {
		return r.Name == claim.Status.Requests[i].Name
	})
	if j == -1 {
		return nil
	}

	return &requests[j]
}

// expectClaimGone asserts that the claim key does not exist. While it exists,
// a failure names each condition of the claim and of its requests that is
// not True. It is written for Eventually.
func expectClaimGone(g Gomega, key client.ObjectKey) {
	var claim fleetv1alpha1.CamundaClusterClaim
	err := k8s.Get(ctx, key, &claim)
	report := ""
	if err == nil {
		requests, err := claimRequests(&claim)
		g.Expect(err).NotTo(HaveOccurred())
		report = describeClaim(&claim, requests)
	}

	g.Expect(err).To(Satisfy(apierrors.IsNotFound), "the claim is not gone\n%s", report)
}
