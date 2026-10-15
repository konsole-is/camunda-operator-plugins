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

package version

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestVersionWithoutStampIsDev(t *testing.T) {
	if os.Getenv("WANT_VERSION") != "" {
		t.Skip("a stamped build runs TestVersionReportsStampedTag")
	}

	assert.Equal(t, Dev, Version())
}

// TestVersionReportsStampedTag runs only from make test-version-stamp, which
// builds the test with the ldflags of a release build.
func TestVersionReportsStampedTag(t *testing.T) {
	want := os.Getenv("WANT_VERSION")
	if want == "" {
		t.Skip("WANT_VERSION is not set")
	}

	assert.Equal(t, want, Version())
}
