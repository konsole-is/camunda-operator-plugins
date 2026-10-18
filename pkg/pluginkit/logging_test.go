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

package pluginkit

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

func TestNewLoggerWritesJSONInDevelopmentMode(t *testing.T) {
	var buf bytes.Buffer

	NewLogger(&zap.Options{Development: true, DestWriter: &buf}).Info("x", "k", "v")

	line := bytes.TrimSpace(buf.Bytes())
	require.True(t, json.Valid(line), "the line %q is not JSON", line)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(line, &decoded))
	assert.Equal(t, "x", decoded["msg"])
	assert.Equal(t, "v", decoded["k"])
}
