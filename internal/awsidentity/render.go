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

package awsidentity

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"

	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

// templateMarker marks a parameter of a registration schema whose value is
// rendered before a grant template reads it.
const templateMarker = "x-fleet-template"

// renderGrant returns the fragment of the grant template named template of
// registration for request, with its parameters and status.outputs. It is
// the fragment that pluginkit writes, so a Grant with another fragment no
// longer matches its template. It refuses a template that registration does
// not hold, and an output that the template reads and request has not.
//
// pluginkit renders the same way when it writes a Grant, from unexported
// code. A change there must change this function too.
func renderGrant(
	request *pluginsv1alpha1.PluginRequest,
	registration *pluginsv1alpha1.PluginRegistration,
	template string,
) ([]byte, error) {
	i := slices.IndexFunc(registration.Spec.GrantTemplates, func(t pluginsv1alpha1.GrantTemplate) bool {
		return t.Name == template
	})
	if i == -1 {
		return nil, fmt.Errorf("the registration %s has no grant template %q", registration.Name, template)
	}

	tmpl := registration.Spec.GrantTemplates[i]
	if tmpl.Template == nil {
		return nil, fmt.Errorf("the grant template %q has no template", template)
	}

	data := pluginkit.TemplateData{
		Namespace:      request.Namespace,
		Claim:          request.Spec.ClaimRef.Name,
		Request:        request.Name,
		ServiceAccount: request.Spec.Target.ServiceAccountName,
	}
	parameters, err := decodeParameters(request.Spec.Parameters)
	if err != nil {
		return nil, err
	}
	data.Parameters = parameters

	if err := renderTemplateParameters(registration.Spec.Parameters, data); err != nil {
		return nil, err
	}
	data.Outputs = request.Status.Outputs

	return pluginkit.RenderJSON(tmpl.Template.Raw, pluginkit.TemplateGrant, data)
}

func decodeParameters(parameters *apiextensionsv1.JSON) (map[string]any, error) {
	values := map[string]any{}
	if parameters == nil || len(parameters.Raw) == 0 {
		return values, nil
	}

	decoder := json.NewDecoder(bytes.NewReader(parameters.Raw))
	decoder.UseNumber()
	if err := decoder.Decode(&values); err != nil {
		return nil, fmt.Errorf("decoding spec.parameters: %w", err)
	}
	return values, nil
}

// renderTemplateParameters renders, in data.Parameters, the value of each
// top-level parameter that schema marks with templateMarker.
func renderTemplateParameters(schema *apiextensionsv1.JSON, data pluginkit.TemplateData) error {
	if schema == nil || len(schema.Raw) == 0 {
		return nil
	}

	var doc struct {
		Properties map[string]map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(schema.Raw, &doc); err != nil {
		return fmt.Errorf("decoding the parameters schema: %w", err)
	}

	for name, property := range doc.Properties {
		value, ok := data.Parameters[name]
		if marked, _ := property[templateMarker].(bool); !marked || !ok {
			continue
		}

		raw, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("encoding parameter %q: %w", name, err)
		}

		rendered, err := pluginkit.RenderJSON(raw, pluginkit.TemplateParameter, data)
		if err != nil {
			return fmt.Errorf("rendering parameter %q: %w", name, err)
		}

		var v any
		decoder := json.NewDecoder(bytes.NewReader(rendered))
		decoder.UseNumber()
		if err := decoder.Decode(&v); err != nil {
			return fmt.Errorf("decoding parameter %q: %w", name, err)
		}
		data.Parameters[name] = v
	}

	return nil
}
