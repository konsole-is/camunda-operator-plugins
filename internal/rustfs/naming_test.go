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

package rustfs

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResourceName(t *testing.T) {
	valid := regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)

	short := resourceName("team-a", "orders-backup-storage")
	assert.Regexp(t, `^team-a-orders-backup-storage-[0-9a-f]{16}$`, short)
	assert.Equal(t, short, resourceName("team-a", "orders-backup-storage"), "the name is stable")

	long := resourceName(strings.Repeat("n", 63), strings.Repeat("r", 63))
	assert.Len(t, long, 63)
	assert.Regexp(t, valid, long)

	assert.NotEqual(t, resourceName("a-b", "c"), resourceName("a", "b-c"))
	assert.NotEqual(
		t,
		resourceName(strings.Repeat("n", 63), strings.Repeat("r", 62)+"a"),
		resourceName(strings.Repeat("n", 63), strings.Repeat("r", 62)+"b"),
		"two names that differ after the cut",
	)

	assert.NotEqual(
		t,
		resourceName(strings.Repeat("n", 56)+"a", "orders-119-backup-storage"),
		resourceName(strings.Repeat("n", 56)+"b", "orders-295-backup-storage"),
		"two names whose hashes share their first 6 hex characters",
	)

	dotted := resourceName("team-a", "orders.v2")
	assert.Regexp(t, valid, dotted)
	assert.NotContains(t, dotted, ".")

	// A cut that ends on a hyphen must not leave two hyphens before the hash.
	assert.Regexp(t, valid, resourceName(strings.Repeat("n", 45), "r-request"))
	assert.NotContains(t, resourceName(strings.Repeat("n", 45), "r-request"), "--")
}

func TestSecretName(t *testing.T) {
	assert.Equal(t, "orders-rustfs", secretName("orders"))
}
