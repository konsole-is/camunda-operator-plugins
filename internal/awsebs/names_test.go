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

package awsebs

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/util/validation"
)

func TestStorageClassNameCarriesAHashOfTheRequest(t *testing.T) {
	name := StorageClassName("team-a", "orders", "orders-volume-encryption")

	assert.Regexp(t, `^camunda-team-a-orders-[0-9a-f]{16}$`, name)
	assert.Equal(t, name, StorageClassName("team-a", "orders", "orders-volume-encryption"))
}

func TestStorageClassNameKeepsApartTwoRequestsWhoseJoinedNamesReadTheSame(t *testing.T) {
	a := StorageClassName("a-b", "c", "c-volume-encryption")
	b := StorageClassName("a", "b-c", "b-c-volume-encryption")

	assert.True(t, strings.HasPrefix(a, "camunda-a-b-c-"))
	assert.True(t, strings.HasPrefix(b, "camunda-a-b-c-"))
	assert.NotEqual(t, a, b)
}

func TestStorageClassNameKeepsApartOneRequestNameInTwoNamespaces(t *testing.T) {
	assert.NotEqual(t,
		StorageClassName("team-a", "orders", "orders-volume-encryption"),
		StorageClassName("team-b", "orders", "orders-volume-encryption"),
	)
}

func TestStorageClassNameFitsALongNamespaceAndClaim(t *testing.T) {
	namespace := strings.Repeat("n", 63)
	claim := strings.Repeat("c.", 120) + "end"

	name := StorageClassName(namespace, claim, claim+"-volume-encryption")

	assert.LessOrEqual(t, len(name), 63)
	assert.Empty(t, validation.IsDNS1123Label(name))
	other := StorageClassName(namespace, claim+"x", claim+"x-volume-encryption")
	assert.NotEqual(t, name, other, "two claims that differ after the cut get two names")
}

func TestStorageClassNameReplacesTheDotsOfTheClaim(t *testing.T) {
	assert.Regexp(t, `^camunda-team-a-orders-eu-[0-9a-f]{16}$`,
		StorageClassName("team-a", "orders.eu", "orders.eu-volume-encryption"))
}
