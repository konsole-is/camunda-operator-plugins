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

package awss3_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

func TestChartRegion(t *testing.T) {
	registrations, err := chartRegistrations(t.TempDir())
	require.NoError(t, err)
	schema := registrations[0].Spec.Parameters

	for _, region := range []string{"", "eu-west-1", "us-gov-west-1"} {
		assert.NoError(t, pluginkit.ValidateParameters(context.Background(), schema, raw(`{"region":"`+region+`"}`)),
			"region %q", region)
	}
	assert.ErrorContains(t, pluginkit.ValidateParameters(context.Background(), schema, raw(`{"region":"EU"}`)),
		"spec.parameters.region")
}

func TestChartBucketPrefix(t *testing.T) {
	registrations, err := chartRegistrations(t.TempDir())
	require.NoError(t, err)
	require.Len(t, registrations, 1)
	schema := registrations[0].Spec.Parameters

	const reserved = "S3 reserves the bucket name prefixes"
	tests := []struct {
		prefix, refusal string
	}{
		{"camunda", ""},
		{"xn", ""},
		{"sthreex", ""},
		{"sthree", reserved},
		{"sthree-backups", reserved},
		{"amzn-s3-demo", reserved},
		{"xn--a", reserved},
		{"Camunda", "spec.parameters.bucketPrefix"},
	}
	for _, tt := range tests {
		t.Run(tt.prefix, func(t *testing.T) {
			err := pluginkit.ValidateParameters(context.Background(), schema, raw(`{"bucketPrefix":"`+tt.prefix+`"}`))
			if tt.refusal == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, tt.refusal)
		})
	}
}
