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
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"

	utiljson "k8s.io/apimachinery/pkg/util/json"
	"k8s.io/apimachinery/pkg/util/validation"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// The outputs of an access request. Every request carries all four.
const (
	OutputExternalURL    = "externalUrl"
	OutputGRPCHost       = "grpcHost"
	OutputOptimizeURL    = "optimizeUrl"
	OutputManagementHost = "managementHost"
)

// The schemes of the outputs.
const (
	schemeHTTP  = "http"
	schemeHTTPS = "https"
)

// GatewayRef names the Gateway that the routes of a request attach to.
type GatewayRef struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// Params are the parameters of an access request. Each pattern takes the
// placeholders {name}, the name of the claim, and {namespace}, the namespace
// of the request.
type Params struct {
	GatewayRef GatewayRef `json:"gatewayRef"`
	// ListenerName, when set, attaches the routes to that listener of the
	// Gateway only.
	ListenerName string `json:"listenerName,omitempty"`
	// Scheme is http or https. Empty is https.
	Scheme                    string `json:"scheme,omitempty"`
	HostnamePattern           string `json:"hostnamePattern"`
	GRPCHostnamePattern       string `json:"grpcHostnamePattern"`
	OptimizeHostnamePattern   string `json:"optimizeHostnamePattern"`
	ManagementHostnamePattern string `json:"managementHostnamePattern"`
	// PolicyTemplates are the policies that the plugin applies to each route.
	PolicyTemplates []PolicyTemplate `json:"policyTemplates,omitempty"`
	// RouteFilters are the ExtensionRef filters of each rule of each route,
	// in this order.
	RouteFilters []gatewayv1.LocalObjectReference `json:"routeFilters,omitempty"`

	// values holds every parameter of the request, also those that Params
	// has no field for, so that a policy template can name them.
	values map[string]any
}

// UnmarshalJSON decodes the fields of p, and keeps every parameter for the
// references of the policy templates. An integer of a parameter decodes as an
// int64, as in an object that the API server returns.
func (p *Params) UnmarshalJSON(data []byte) error {
	type plain Params
	var fields plain
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}

	var values map[string]any
	if err := utiljson.Unmarshal(data, &values); err != nil {
		return err
	}

	*p = Params(fields)
	p.values = values
	return nil
}

func (p Params) scheme() string {
	if p.Scheme == "" {
		return schemeHTTPS
	}
	return p.Scheme
}

// Hosts are the host names of a request, one for each pattern of Params.
type Hosts struct {
	Cluster    string
	GRPC       string
	Optimize   string
	Management string
}

// RenderHosts fills each pattern of p with claim for {name} and namespace for
// {namespace}. The error names each pattern that is empty or that gives a host
// that is not a DNS name, with that host.
func RenderHosts(p Params, claim, namespace string) (Hosts, error) {
	replacer := strings.NewReplacer("{name}", claim, "{namespace}", namespace)

	var errs []error
	render := func(parameter, pattern string) string {
		if pattern == "" {
			errs = append(errs, fmt.Errorf("%s is empty", parameter))
			return ""
		}

		host := replacer.Replace(pattern)
		if problems := dnsProblems(host); len(problems) > 0 {
			errs = append(errs, fmt.Errorf(
				"%s gives the host %q, which is not a DNS name: %s",
				parameter,
				host,
				strings.Join(problems, "; "),
			))
		}
		return host
	}

	h := Hosts{
		Cluster:    render("hostnamePattern", p.HostnamePattern),
		GRPC:       render("grpcHostnamePattern", p.GRPCHostnamePattern),
		Optimize:   render("optimizeHostnamePattern", p.OptimizeHostnamePattern),
		Management: render("managementHostnamePattern", p.ManagementHostnamePattern),
	}
	if err := errors.Join(errs...); err != nil {
		return Hosts{}, err
	}

	return h, nil
}

func dnsProblems(host string) []string {
	problems := validation.IsDNS1123Subdomain(host)
	for label := range strings.SplitSeq(host, ".") {
		problems = append(problems, validation.IsDNS1123Label(label)...)
	}
	// The hostnames of a Gateway API route must not be IP addresses.
	if _, err := netip.ParseAddr(host); err == nil {
		problems = append(problems, "it is an IP address")
	}
	return problems
}

// Outputs returns the four outputs of a request with the hosts h.
func Outputs(p Params, h Hosts) map[string]string {
	return map[string]string{
		OutputExternalURL:    p.scheme() + "://" + h.Cluster,
		OutputGRPCHost:       h.GRPC,
		OutputOptimizeURL:    p.scheme() + "://" + h.Optimize,
		OutputManagementHost: h.Management,
	}
}
