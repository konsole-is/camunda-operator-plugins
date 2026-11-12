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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestResourceNameStaysTheSame pins names of live buckets: a changed name
// orphans the bucket, the user, and the policy of each request.
func TestResourceNameStaysTheSame(t *testing.T) {
	for _, c := range []struct{ namespace, request, want string }{
		{"team-a", "orders-backup-storage", "team-a-orders-backup-storage-666dc827e038bd19"},
		{"team-a", "orders.v2", "team-a-orders-v2-0218db33765d3cb3"},
		{
			strings.Repeat("n", 63),
			strings.Repeat("r", 63),
			strings.Repeat("n", 46) + "-454433ce982a8c57",
		},
		{strings.Repeat("n", 45), "r-request", strings.Repeat("n", 45) + "-66f76ed1259337e4"},
	} {
		assert.Equal(t, c.want, resourceName(c.namespace, c.request))
	}
}

func TestSecretName(t *testing.T) {
	assert.Equal(t, "orders-rustfs", secretName("orders"))
}
