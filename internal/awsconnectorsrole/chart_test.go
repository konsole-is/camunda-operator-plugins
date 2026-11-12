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

package awsconnectorsrole

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

func TestChartPolicyARNPatternTakesOnlyWhatIAMAccepts(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "charts", "aws-connectors-role", "values.yaml"))
	require.NoError(t, err)
	var values struct {
		Registrations []struct {
			Name       string `json:"name"`
			Parameters struct {
				Properties map[string]struct {
					Items struct {
						Pattern string `json:"pattern"`
					} `json:"items"`
				} `json:"properties"`
			} `json:"parameters"`
		} `json:"registrations"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &values))

	accepted := []string{
		"arn:aws:iam::aws:policy/service-role/AmazonEKSClusterPolicy",
		"arn:aws:iam::111122223333:policy/connectors",
		"arn:aws:iam::111122223333:policy/team.a/b+c=d,e@f_g-h",
		"arn:aws:iam::111122223333:policy/" + strings.Repeat("a", 128),
		"arn:aws-us-gov:iam::111122223333:policy/connectors",
	}
	refused := map[string]string{
		"two ARNs in one item": "arn:aws:iam::111122223333:policy/a,arn:aws:iam::111122223333:policy/b",
		"a space in the name":  "arn:aws:iam::111122223333:policy/a b",
		"a name over 128":      "arn:aws:iam::111122223333:policy/" + strings.Repeat("a", 129),
		"no name":              "arn:aws:iam::111122223333:policy/team/",
	}

	var checked int
	for _, r := range values.Registrations {
		for _, field := range []string{"managedPolicyArns", "extraPolicyArns"} {
			pattern := r.Parameters.Properties[field].Items.Pattern
			if pattern == "" {
				continue
			}
			checked++
			re := regexp.MustCompile(pattern)
			for _, arn := range accepted {
				assert.True(t, re.MatchString(arn), "%s accepts %s", field, arn)
			}
			for name, arn := range refused {
				assert.False(t, re.MatchString(arn), "%s refuses %s", field, name)
			}
		}
	}
	assert.Equal(t, 2, checked, "both policy lists have a pattern")
}
