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
	"testing"

	"github.com/stretchr/testify/assert"
)

// deploymentWithLease renders a Deployment name in camunda-operator-plugins
// whose controller elects a leader with the Lease lease.
func deploymentWithLease(name, lease string) string {
	return `
apiVersion: apps/v1
kind: Deployment
metadata: {name: ` + name + `, namespace: camunda-operator-plugins}
spec:
  template:
    spec:
      containers: [{name: controller, image: example, args: [-leader-elect, -leader-election-id=` + lease + `]}]
`
}

func clusterRole(name string) string {
	return "apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRole\nmetadata: {name: " + name + "}\n"
}

func TestObjectNamesListsEachObjectAndTheLeaseOfEachDeployment(t *testing.T) {
	objects := render(t, deploymentWithLease("aws", "aws"), clusterRole("ns-aws-1a2b3c4d-identity-serve"))

	assert.Equal(t, []string{
		"ClusterRole -/ns-aws-1a2b3c4d-identity-serve",
		"Deployment camunda-operator-plugins/aws",
		"Lease camunda-operator-plugins/aws",
	}, objectNames(objects, "camunda-operator-plugins"))
}

func TestObjectNamesPlacesALeaseWithoutANamespaceInTheReleaseNamespace(t *testing.T) {
	objects := render(t, `
apiVersion: apps/v1
kind: Deployment
metadata: {name: aws}
spec:
  template:
    spec:
      containers: [{name: controller, image: example, args: [-leader-elect, -leader-election-id=aws]}]
`)

	assert.Contains(t, objectNames(objects, "plugins"), "Lease plugins/aws")
}

// Two releases that leave out -leader-election-id share the default Lease of
// the binary, so both list the same line.
func TestObjectNamesListsTheDefaultLeaseOfADeploymentWithoutAnID(t *testing.T) {
	objects := render(t, `
apiVersion: apps/v1
kind: Deployment
metadata: {name: aws, namespace: plugins}
spec:
  template:
    spec:
      containers: [{name: controller, image: example, args: [-leader-elect]}]
`)

	assert.Contains(t, objectNames(objects, "plugins"), "Lease plugins/(default)")
}

func TestObjectNamesListsNoLeaseForADeploymentWithoutLeaderElection(t *testing.T) {
	objects := render(t, `
apiVersion: apps/v1
kind: Deployment
metadata: {name: aws, namespace: plugins}
spec:
  template:
    spec:
      containers: [{name: controller, image: example, args: [-leader-election-id=aws]}]
`)

	assert.Equal(t, []string{"Deployment plugins/aws"}, objectNames(objects, "plugins"))
}
