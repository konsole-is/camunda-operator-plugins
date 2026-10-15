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

// Command chartcheck checks the rendered chart of a plugin against the chart
// rule of CONTRIBUTING.md. make chart-verify pipes helm template into it.
//
// The chart must render a Deployment that runs as a ServiceAccount of the
// chart, a ClusterRole that grants the verb serve on pluginregistrations
// with resourceNames set to exactly the registrations of the values, and a
// ClusterRoleBinding of that ClusterRole to the controller ServiceAccount. It
// must render no ValidatingAdmissionPolicy and no
// ValidatingAdmissionPolicyBinding, because the operator chart holds the one
// grant admission policy.
//
// Usage: helm template ... | chartcheck -values <values file> -namespace <release namespace>
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
)

func main() {
	values := flag.String(
		"values",
		"",
		"the values file of the render; its registrations[].name are the registrations of the chart",
	)
	namespace := flag.String("namespace", "", "the release namespace of the render")
	flag.Parse()

	if err := run(os.Stdin, *values, *namespace); err != nil {
		fmt.Fprintln(os.Stderr, "chartcheck:", err)
		os.Exit(1)
	}
}

func run(input io.Reader, values, namespace string) error {
	registrations, err := readRegistrations(values)
	if err != nil {
		return err
	}

	objects, err := readObjects(input)
	if err != nil {
		return err
	}

	found := problems(objects, registrations, namespace)
	for _, p := range found {
		fmt.Fprintln(os.Stderr, "chartcheck:", p)
	}
	if len(found) > 0 {
		return fmt.Errorf("%d problems with the chart rule", len(found))
	}
	fmt.Printf("chartcheck: %d objects serve the registrations %v\n", len(objects), registrations)

	return nil
}

// readRegistrations returns the registrations[].name of a values file.
func readRegistrations(file string) ([]string, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("reading the values %s: %w", file, err)
	}

	var values struct {
		Registrations []struct {
			Name string `json:"name"`
		} `json:"registrations"`
	}
	if err := yaml.Unmarshal(data, &values); err != nil {
		return nil, fmt.Errorf("decoding the values %s: %w", file, err)
	}

	names := make([]string, 0, len(values.Registrations))
	for _, r := range values.Registrations {
		names = append(names, r.Name)
	}

	return names, nil
}

// readObjects decodes a stream of YAML documents. It skips an empty
// document, which helm template writes for a template that renders nothing,
// and returns the items of a List document in place of the List.
func readObjects(input io.Reader) ([]*unstructured.Unstructured, error) {
	decoder := utilyaml.NewYAMLOrJSONDecoder(input, 4096)
	var objects []*unstructured.Unstructured
	for {
		var content map[string]any
		err := decoder.Decode(&content)
		if errors.Is(err, io.EOF) {
			return objects, nil
		}
		if err != nil {
			return nil, fmt.Errorf("decoding the rendered chart: %w", err)
		}
		if len(content) == 0 {
			continue
		}

		object := &unstructured.Unstructured{Object: content}
		if !object.IsList() {
			objects = append(objects, object)
			continue
		}

		err = object.EachListItem(func(item runtime.Object) error {
			objects = append(objects, item.(*unstructured.Unstructured))
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("reading the items of %s %q: %w", object.GetKind(), object.GetName(), err)
		}
	}
}
