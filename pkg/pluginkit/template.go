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
	"fmt"
	"strings"
	"text/template"
	"text/template/parse"
)

// TemplateKind is the place of a template in a registration. It sets the
// placeholders that the template can use.
type TemplateKind int

const (
	// TemplateGrant is a grant template.
	TemplateGrant TemplateKind = iota
	// TemplateParameter is a parameter whose schema has "x-fleet-template": true.
	TemplateParameter
	// TemplateMetricQuery is a metric query. It can also use {{ .pods }}.
	TemplateMetricQuery
)

// TemplateData holds the values of the placeholders for one request.
type TemplateData struct {
	Namespace      string
	Claim          string
	Request        string
	ServiceAccount string
	Parameters     map[string]any
	Outputs        map[string]string
	// Pods is the pod regular expression of a metric query.
	Pods string
}

// Template is a parsed template of the plugin contract.
type Template struct {
	tmpl       *template.Template
	parameters []string
	outputs    []string
}

// ParseTemplate parses text as a template of kind. A template holds plain
// text and field references only: {{ .namespace }}, {{ .claim }},
// {{ .request }}, {{ .serviceAccount }}, {{ .parameters.<name> }},
// {{ .outputs.<key> }}, and in a metric query also {{ .pods }}. Each other
// construct is an error that names it.
func ParseTemplate(text string, kind TemplateKind) (*Template, error) {
	// text/template drops comments and moves definitions out of the root,
	// so the check parses the text with its own tree.
	tree := parse.New("")
	tree.Mode = parse.ParseComments | parse.SkipFuncCheck
	trees := map[string]*parse.Tree{}
	if _, err := tree.Parse(text, "", "", trees); err != nil {
		return nil, err
	}

	for name := range trees {
		if name != "" {
			return nil, constructError("define")
		}
	}

	tmpl, err := template.New("").Option("missingkey=error").Parse(text)
	if err != nil {
		return nil, err
	}

	t := &Template{tmpl: tmpl}
	for _, node := range tree.Root.Nodes {
		if err := t.checkNode(node, kind); err != nil {
			return nil, err
		}
	}

	return t, nil
}

func (t *Template) checkNode(node parse.Node, kind TemplateKind) error {
	switch n := node.(type) {
	case *parse.TextNode:
		return nil
	case *parse.ActionNode:
		return t.checkAction(n, kind)
	case *parse.IfNode:
		return constructError("if")
	case *parse.RangeNode:
		return constructError("range")
	case *parse.WithNode:
		return constructError("with")
	case *parse.TemplateNode:
		return constructError("template")
	case *parse.CommentNode:
		return constructError("comment")
	default:
		return fmt.Errorf("the construct %q is not allowed: a template holds field references only", node.String())
	}
}

func constructError(name string) error {
	return fmt.Errorf("{{ %s }} is not allowed: a template holds field references only", name)
}

func (t *Template) checkAction(action *parse.ActionNode, kind TemplateKind) error {
	pipe := action.Pipe
	if len(pipe.Decl) > 0 {
		return fmt.Errorf("a variable is not allowed in %s: a template holds field references only", action)
	}

	if len(pipe.Cmds) != 1 {
		return fmt.Errorf("a pipeline is not allowed in %s: a template holds field references only", action)
	}

	args := pipe.Cmds[0].Args
	if ident, ok := args[0].(*parse.IdentifierNode); ok {
		return fmt.Errorf("the function %s is not allowed: a template holds field references only", ident.Ident)
	}

	field, ok := args[0].(*parse.FieldNode)
	if !ok || len(args) != 1 {
		return fmt.Errorf("%s is not allowed: a template holds field references only", action)
	}

	return t.checkField(field.Ident, kind)
}

func (t *Template) checkField(idents []string, kind TemplateKind) error {
	name := "." + strings.Join(idents, ".")
	switch {
	case len(idents) == 1 && (idents[0] == "parameters" || idents[0] == "outputs"):
		return fmt.Errorf("the placeholder %s needs a name, as in %s.<name>", name, name)
	case len(idents) == 1 && idents[0] == "pods":
		if kind != TemplateMetricQuery {
			return fmt.Errorf("the placeholder .pods is allowed only in a metric query")
		}
		return nil
	case len(idents) == 1:
		switch idents[0] {
		case "namespace", "claim", "request", "serviceAccount":
			return nil
		}
	case len(idents) == 2 && idents[0] == "parameters":
		t.parameters = append(t.parameters, idents[1])
		return nil
	case len(idents) == 2 && idents[0] == "outputs":
		t.outputs = append(t.outputs, idents[1])
		return nil
	}

	return fmt.Errorf("unknown placeholder %s", name)
}

// Render fills the placeholders of t from data. A parameter or an output
// that the template references and that data does not hold is an error that
// names it.
func (t *Template) Render(data TemplateData) (string, error) {
	for _, name := range t.parameters {
		if _, ok := data.Parameters[name]; !ok {
			return "", fmt.Errorf("parameter %q has no value", name)
		}
	}

	for _, key := range t.outputs {
		if _, ok := data.Outputs[key]; !ok {
			return "", fmt.Errorf("output %q has no value", key)
		}
	}

	var out bytes.Buffer
	err := t.tmpl.Execute(&out, map[string]any{
		"namespace":      data.Namespace,
		"claim":          data.Claim,
		"request":        data.Request,
		"serviceAccount": data.ServiceAccount,
		"parameters":     data.Parameters,
		"outputs":        data.Outputs,
		"pods":           data.Pods,
	})
	if err != nil {
		return "", err
	}

	return out.String(), nil
}

// RenderJSON renders each string value of the JSON document raw as a
// template of kind, and returns the document as JSON again. Keys are not
// rendered, and the other values keep their value. A rendered value stays
// inside its string, so a quote in a parameter or an output cannot change the
// structure of the document.
func RenderJSON(raw []byte, kind TemplateKind, data TemplateData) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var doc any
	if err := decoder.Decode(&doc); err != nil {
		return nil, fmt.Errorf("decoding the template: %w", err)
	}

	rendered, err := renderValue(doc, kind, data)
	if err != nil {
		return nil, err
	}

	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(rendered); err != nil {
		return nil, fmt.Errorf("encoding the rendered template: %w", err)
	}

	return bytes.TrimSuffix(out.Bytes(), []byte("\n")), nil
}

func renderValue(value any, kind TemplateKind, data TemplateData) (any, error) {
	switch v := value.(type) {
	case string:
		tmpl, err := ParseTemplate(v, kind)
		if err != nil {
			return nil, err
		}
		return tmpl.Render(data)
	case []any:
		for i := range v {
			rendered, err := renderValue(v[i], kind, data)
			if err != nil {
				return nil, err
			}
			v[i] = rendered
		}
		return v, nil
	case map[string]any:
		for key, item := range v {
			rendered, err := renderValue(item, kind, data)
			if err != nil {
				return nil, err
			}
			v[key] = rendered
		}
		return v, nil
	default:
		return v, nil
	}
}
