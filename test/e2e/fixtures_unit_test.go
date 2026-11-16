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
)

func TestKeysOfBackupMatchesBackupIDAsPathSegment(t *testing.T) {
	keys := []string{
		"aws/orders/1/1763300000000/0/manifest.json",
		"aws/orders/2/1763300000000/0/contents/snapshot/000001.sst",
		"aws/orders/1/1763299999999/0/manifest.json",
		"aws/orders/1/17633000000001/0/manifest.json",
		"elasticsearch/snap-1763300000000.dat",
	}

	assert.Equal(t, keys[:2], keysOfBackup(keys, 1763300000000))
}

func TestKeysOfBackupIsEmptyWithoutTheBackupID(t *testing.T) {
	keys := []string{"aws/orders/1/1763299999999/0/manifest.json", "elasticsearch/index-0"}

	assert.Empty(t, keysOfBackup(keys, 1763300000000))
}
