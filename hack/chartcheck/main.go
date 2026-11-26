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
// chart, ClusterRoles that grant the verb serve on pluginregistrations with
// resourceNames set to exactly the registrations of the values, and
// ClusterRoleBindings of those ClusterRoles to the controller
// ServiceAccount. It must render no ValidatingAdmissionPolicy and no
// ValidatingAdmissionPolicyBinding, because the operator chart holds the one
// grant admission policy.
//
// The values are one or more files, merged in order as helm merges its -f
// files. The registrations of the values are the names of the top-level
// registrations, and the names of <block>.registrations for each top-level
// block whose enabled is true: a chart with more than one controller serves
// the registrations of each controller that is on.
//
// With -names, chartcheck checks nothing. It prints one line for each
// rendered object, and one for the leader election Lease of each Deployment,
// so that make chart-verify can compare the names of two releases.
//
// Usage:
//
//	helm template ... | chartcheck -values <values file> [-values <values file>]... -namespace <release namespace>
//	helm template ... | chartcheck -names -namespace <release namespace>
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
)

// valueFiles are the files of a repeated -values flag, in the order given.
type valueFiles []string

func (v *valueFiles) String() string {
	return strings.Join(*v, ",")
}

func (v *valueFiles) Set(file string) error {
	*v = append(*v, file)
	return nil
}

func main() {
	var values valueFiles
	flag.Var(
		&values,
		"values",
		"a values file of the render; repeat it for each -f of helm template, in the same order",
	)
	names := flag.Bool("names", false, "print the name of each object and Lease of the render instead of the check")
	namespace := flag.String("namespace", "", "the release namespace of the render")
	flag.Parse()

	var err error
	if *names {
		err = printNames(os.Stdin, *namespace)
	} else {
		err = run(os.Stdin, values, *namespace)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "chartcheck:", err)
		os.Exit(1)
	}
}

func run(input io.Reader, values []string, namespace string) error {
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

func printNames(input io.Reader, namespace string) error {
	objects, err := readObjects(input)
	if err != nil {
		return err
	}

	for _, line := range objectNames(objects, namespace) {
		fmt.Println(line)
	}

	return nil
}

// readRegistrations returns the registrations of the values files, merged
// in order: the names of the top-level registrations, then the names of
// <block>.registrations for each top-level block whose enabled is true, in
// the order of the block keys.
func readRegistrations(files []string) ([]string, error) {
	merged := map[string]any{}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("reading the values %s: %w", file, err)
		}

		var values map[string]any
		if err := yaml.Unmarshal(data, &values); err != nil {
			return nil, fmt.Errorf("decoding the values %s: %w", file, err)
		}
		mergeValues(merged, values)
	}

	names := registrationNames(merged["registrations"])
	for _, key := range slices.Sorted(maps.Keys(merged)) {
		block, ok := merged[key].(map[string]any)
		if ok && block["enabled"] == true {
			names = append(names, registrationNames(block["registrations"])...)
		}
	}

	return names, nil
}

// mergeValues merges src into dst as helm merges a later values file: a map
// merges key by key, and any other value replaces the value of dst.
func mergeValues(dst, src map[string]any) {
	for key, value := range src {
		from, isMap := value.(map[string]any)
		into, wasMap := dst[key].(map[string]any)
		if isMap && wasMap {
			mergeValues(into, from)
			continue
		}
		dst[key] = value
	}
}

// registrationNames returns the name of each item of a registrations list.
func registrationNames(list any) []string {
	items, _ := list.([]any)
	names := make([]string, 0, len(items))
	for _, item := range items {
		registration, _ := item.(map[string]any)
		name, _ := registration["name"].(string)
		names = append(names, name)
	}

	return names
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
