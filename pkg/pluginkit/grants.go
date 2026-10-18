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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
	v1 "github.com/konsole-is/camunda-operator/api/v1"
)

// targetKindCluster is the target kind of a request whose grants name a
// cluster.
const targetKindCluster = "CamundaCluster"

// GrantName returns the name of the Grant of a request for one grant
// template. The name has at most 63 characters, and two templates of one
// request get two names.
func GrantName(request, template string) string {
	name := request + "-" + template
	if len(name) <= 63 {
		return name
	}

	sum := sha256.Sum256([]byte(request + "/" + template))
	return strings.TrimRight(name[:54], "-") + "-" + hex.EncodeToString(sum[:])[:8]
}

// renderGrants returns one Grant for each grant template of reg whose
// required inputs all have a value in req. A registration with grant
// templates needs a request whose target is a CamundaCluster.
func renderGrants(
	req *pluginsv1alpha1.PluginRequest,
	reg *pluginsv1alpha1.PluginRegistration,
	data TemplateData,
) ([]*pluginsv1alpha1.Grant, error) {
	if len(reg.Spec.GrantTemplates) > 0 && req.Spec.Target.Kind != targetKindCluster {
		return nil, fmt.Errorf(
			"grants need a %s target, and the target is a %s",
			targetKindCluster,
			req.Spec.Target.Kind,
		)
	}

	var grants []*pluginsv1alpha1.Grant
	for _, tmpl := range reg.Spec.GrantTemplates {
		if !hasInputs(req, tmpl.RequiresInputs) {
			continue
		}

		if tmpl.Template == nil {
			return nil, fmt.Errorf("grant template %q has no template", tmpl.Name)
		}

		fragment, err := RenderJSON(tmpl.Template.Raw, TemplateGrant, data)
		if err != nil {
			return nil, fmt.Errorf("rendering grant template %q: %w", tmpl.Name, err)
		}

		grants = append(grants, &pluginsv1alpha1.Grant{
			ObjectMeta: metav1.ObjectMeta{
				Name:      GrantName(req.Name, tmpl.Name),
				Namespace: req.Namespace,
				Labels: map[string]string{
					pluginsv1alpha1.LabelRegistration: req.Spec.Registration,
					pluginsv1alpha1.LabelRequest:      req.Name,
					pluginsv1alpha1.LabelCluster:      req.Spec.Target.Name,
				},
			},
			Spec: pluginsv1alpha1.GrantSpec{
				Cluster:  v1.ClusterRef{Name: req.Spec.Target.Name},
				Template: tmpl.Name,
				Format:   tmpl.Format,
				Fragment: &apiextensionsv1.JSON{Raw: fragment},
			},
		})
	}

	return grants, nil
}

func hasInputs(req *pluginsv1alpha1.PluginRequest, names []string) bool {
	for _, name := range names {
		if req.Spec.Inputs[name] == "" {
			return false
		}
	}
	return true
}
