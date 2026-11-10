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

package awsebs_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

func TestChartParameters(t *testing.T) {
	registrations, err := chartRegistrations(t.TempDir())
	require.NoError(t, err)
	require.Len(t, registrations, 1)
	schema := registrations[0].Spec.Parameters

	tests := []struct {
		name, parameters, refusal string
	}{
		{"the defaults", `{}`, ""},
		{"gp3 with iops and throughput", `{"type":"gp3","iops":6000,"throughput":500}`, ""},
		{"io2 with iops", `{"type":"io2","iops":6000}`, ""},
		{"io1 with the least iops", `{"type":"io1","iops":100}`, ""},
		{"gp3 with the least iops", `{"type":"gp3","iops":3000}`, ""},
		{"gp3 below its least iops", `{"type":"gp3","iops":2999}`, "iops of gp3 is at least 3000"},
		{"the default type below the least iops of gp3", `{"iops":100}`, "iops of gp3 is at least 3000"},
		{"throughput of io2", `{"type":"io2","throughput":500}`, "throughput is only for the volume type gp3"},
		{"iops of gp2", `{"type":"gp2","iops":3000}`, "iops is only for the volume types gp3, io1, and io2"},
		{"an unknown volume type", `{"type":"magnetic"}`, "spec.parameters.type"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := pluginkit.ValidateParameters(
				context.Background(),
				schema,
				&apiextensionsv1.JSON{Raw: []byte(tt.parameters)},
			)
			if tt.refusal == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, tt.refusal)
		})
	}
}
