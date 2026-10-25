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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/konsole-is/camunda-operator/api/v1"
)

func validSettings() Settings {
	return Settings{
		Endpoint:       "http://rustfs.rustfs.svc:9000",
		Region:         "eu-west-1",
		ConsoleURL:     "https://console.example.com",
		AdminAccessKey: "admin",
		AdminSecretKey: "admin-secret",
	}
}

func TestSettingsValidate(t *testing.T) {
	require.NoError(t, validSettings().validate())

	noConsole := validSettings()
	noConsole.ConsoleURL = ""
	require.NoError(t, noConsole.validate(), "the console URL is optional")

	for _, tc := range []struct {
		name   string
		change func(*Settings)
		want   string
	}{
		{"an empty endpoint", func(s *Settings) { s.Endpoint = "" }, "endpoint"},
		{"an endpoint without a scheme", func(s *Settings) { s.Endpoint = "rustfs:9000" }, "endpoint"},
		{"an endpoint that is not http", func(s *Settings) { s.Endpoint = "ftp://rustfs:9000" }, "endpoint"},
		{"a relative console URL", func(s *Settings) { s.ConsoleURL = "/console" }, "consoleUrl"},
		{"an empty admin access key", func(s *Settings) { s.AdminAccessKey = "" }, "admin access key"},
		{"an empty admin secret key", func(s *Settings) { s.AdminSecretKey = "" }, "admin secret key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := validSettings()
			tc.change(&s)
			assert.ErrorContains(t, s.validate(), tc.want)
		})
	}

	t.Run("each problem is named", func(t *testing.T) {
		err := Settings{}.validate()
		for _, want := range []string{"endpoint", "admin access key", "admin secret key"} {
			assert.ErrorContains(t, err, want)
		}
	})
}

func TestSettingsRegion(t *testing.T) {
	assert.Equal(t, "eu-west-1", validSettings().region())
	assert.Equal(t, v1.PlaceholderS3Region, Settings{}.region())
}
