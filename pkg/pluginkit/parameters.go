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
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema/cel"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema/listtype"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema/objectmeta"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/validation"
	utiljson "k8s.io/apimachinery/pkg/util/json"
	"k8s.io/apimachinery/pkg/util/validation/field"
	celconfig "k8s.io/apiserver/pkg/apis/cel"
)

// templateMarker marks a parameter of a registration schema whose value
// pluginkit renders as a template.
const templateMarker = "x-fleet-template"

// ValidateParameters checks parameters against the OpenAPI schema of a
// registration, as the API server checks a custom resource: the schema, the
// x-kubernetes-validations rules, the list types, and the checks of an
// embedded resource. A nil schema accepts every value, and absent or null
// parameters are checked as an empty object. The error names each failing
// field by its path under spec.parameters, and each broken rule by its
// message, or by its text when it has no message. A schema that cannot be
// read is an error too.
func ValidateParameters(ctx context.Context, schema, parameters *apiextensionsv1.JSON) error {
	if schema == nil || len(schema.Raw) == 0 {
		return nil
	}

	var v1Props apiextensionsv1.JSONSchemaProps
	if err := json.Unmarshal(schema.Raw, &v1Props); err != nil {
		return fmt.Errorf("decoding the parameters schema: %w", err)
	}

	var props apiextensions.JSONSchemaProps
	if err := apiextensionsv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(
		&v1Props, &props, nil,
	); err != nil {
		return fmt.Errorf("converting the parameters schema: %w", err)
	}

	validator, _, err := validation.NewSchemaValidator(&props)
	if err != nil {
		return fmt.Errorf("building the validator of the parameters schema: %w", err)
	}

	// The schema validator does not run x-kubernetes-validations, the list
	// types, or the checks of an embedded resource.
	structural, err := structuralschema.NewStructural(&props)
	if err != nil {
		return fmt.Errorf("building the structural schema of the parameters schema: %w", err)
	}

	var value any = map[string]any{}
	if parameters != nil && len(parameters.Raw) > 0 {
		// The API server decodes an integer as int64, as utiljson does.
		if err := utiljson.Unmarshal(parameters.Raw, &value); err != nil {
			return fmt.Errorf("decoding spec.parameters: %w", err)
		}
	}
	// A literal null decodes to nil.
	if value == nil {
		value = map[string]any{}
	}

	path := field.NewPath("spec", "parameters")
	errs := validation.ValidateCustomResource(path, value, validator)
	// NewValidator returns nil for a schema without a rule.
	if rules := cel.NewValidator(structural, false, celconfig.PerCallLimit); rules != nil {
		ruleErrs, _ := rules.Validate(ctx, path, structural, value, nil, celconfig.RuntimeCELCostBudget)
		errs = append(errs, ruleErrs...)
	}
	if object, ok := value.(map[string]any); ok {
		errs = append(errs, listtype.ValidateListSetsAndMaps(path, structural, object)...)
	}
	errs = append(errs, objectmeta.Validate(ctx, path, value, structural, false)...)

	return errs.ToAggregate()
}

// templateParameters returns the sorted names of the top-level properties of
// schema that are marked as templates. It reads the raw JSON, because
// JSONSchemaProps drops vendor extensions.
func templateParameters(schema *apiextensionsv1.JSON) ([]string, error) {
	if schema == nil || len(schema.Raw) == 0 {
		return nil, nil
	}

	var doc struct {
		Properties map[string]map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(schema.Raw, &doc); err != nil {
		return nil, fmt.Errorf("decoding the parameters schema: %w", err)
	}

	var names []string
	for name, property := range doc.Properties {
		if marked, _ := property[templateMarker].(bool); marked {
			names = append(names, name)
		}
	}
	slices.Sort(names)

	return names, nil
}

// renderParameters returns parameters with the value of each template
// parameter of schema rendered: a string value, or each string of an object
// value. The other parameters keep their value. An error names the parameter.
func renderParameters(schema, parameters *apiextensionsv1.JSON, data TemplateData) (*apiextensionsv1.JSON, error) {
	names, err := templateParameters(schema)
	if err != nil {
		return nil, err
	}

	if len(names) == 0 || parameters == nil || len(parameters.Raw) == 0 {
		return parameters, nil
	}

	var values map[string]json.RawMessage
	if err := json.Unmarshal(parameters.Raw, &values); err != nil {
		return nil, fmt.Errorf("decoding spec.parameters: %w", err)
	}

	for _, name := range names {
		value, ok := values[name]
		if !ok {
			continue
		}

		rendered, err := RenderJSON(value, TemplateParameter, data)
		if err != nil {
			return nil, fmt.Errorf("rendering parameter %q: %w", name, err)
		}
		values[name] = rendered
	}

	raw, err := json.Marshal(values)
	if err != nil {
		return nil, fmt.Errorf("encoding spec.parameters: %w", err)
	}

	return &apiextensionsv1.JSON{Raw: raw}, nil
}

// decodeInto decodes raw into into. Nil or empty raw leaves into unchanged.
func decodeInto[T any](raw []byte, into *T) error {
	if len(raw) == 0 {
		return nil
	}

	return json.Unmarshal(raw, into)
}
