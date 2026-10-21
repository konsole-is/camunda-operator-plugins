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

package gatewayaccess

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validParams() Params {
	return Params{
		GatewayRef:                GatewayRef{Namespace: "gateway-system", Name: "public"},
		HostnamePattern:           "{name}.{namespace}.example.com",
		GRPCHostnamePattern:       "grpc-{name}.{namespace}.example.com",
		OptimizeHostnamePattern:   "optimize-{name}.{namespace}.example.com",
		ManagementHostnamePattern: "{name}.platform.example.com",
	}
}

func TestRenderHosts_FillsEachPatternWithNameAndNamespace(t *testing.T) {
	h, err := RenderHosts(validParams(), "orders", "team-a")
	require.NoError(t, err)
	assert.Equal(t, Hosts{
		Cluster:    "orders.team-a.example.com",
		GRPC:       "grpc-orders.team-a.example.com",
		Optimize:   "optimize-orders.team-a.example.com",
		Management: "orders.platform.example.com",
	}, h)
}

func TestRenderHosts_RefusesHostThatIsNotADNSName(t *testing.T) {
	p := validParams()
	p.GRPCHostnamePattern = "{name}-{namespace}.example.com"
	name, namespace := strings.Repeat("a", 40), strings.Repeat("b", 30)

	_, err := RenderHosts(p, name, namespace)
	require.Error(t, err)
	assert.ErrorContains(t, err, "grpcHostnamePattern")
	assert.ErrorContains(t, err, name+"-"+namespace+".example.com")
}

func TestRenderHosts_RefusesAnEmptyPattern(t *testing.T) {
	p := validParams()
	p.OptimizeHostnamePattern = ""

	_, err := RenderHosts(p, "orders", "team-a")
	assert.ErrorContains(t, err, "optimizeHostnamePattern is empty")
}

func TestRenderHosts_NamesEachBadPattern(t *testing.T) {
	p := validParams()
	p.HostnamePattern = "{name}_x.example.com"
	p.ManagementHostnamePattern = ""

	_, err := RenderHosts(p, "orders", "team-a")
	assert.ErrorContains(t, err, "hostnamePattern gives the host \"orders_x.example.com\"")
	assert.ErrorContains(t, err, "managementHostnamePattern is empty")
}

func TestOutputs_WritesAllFourKeysWithTheScheme(t *testing.T) {
	h := Hosts{Cluster: "c.example.com", GRPC: "g.example.com", Optimize: "o.example.com", Management: "m.example.com"}
	assert.Equal(t, map[string]string{
		"externalUrl":    "https://c.example.com",
		"grpcHost":       "g.example.com",
		"optimizeUrl":    "https://o.example.com",
		"managementHost": "m.example.com",
	}, Outputs(Params{}, h))
	assert.Equal(t, "http://c.example.com", Outputs(Params{Scheme: "http"}, h)["externalUrl"])
}
