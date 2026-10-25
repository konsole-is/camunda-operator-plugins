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
	"errors"
	"fmt"
	"regexp"
	"strings"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
)

// Ceiling is the set of IAM actions that the hub puts on a role, from the
// chart value allowedActions. It mirrors the permissions boundary, so that a
// Grant over it is refused before AWS refuses the call at run time.
type Ceiling struct {
	patterns []*regexp.Regexp
}

// pattern is the shape of one allowed action: "<service>:<action>", where
// the action holds letters, digits, and the IAM wildcards * and ?.
var pattern = regexp.MustCompile(`^[A-Za-z0-9-]+:[A-Za-z0-9*?]+$`)

// NewCeiling returns the ceiling of patterns. It refuses an empty list and a
// pattern that is not "<service>:<action>".
func NewCeiling(patterns []string) (Ceiling, error) {
	if len(patterns) == 0 {
		return Ceiling{}, errors.New("the ceiling has no allowed action")
	}

	var c Ceiling
	for _, p := range patterns {
		if !pattern.MatchString(p) {
			return Ceiling{}, fmt.Errorf(
				"the allowed action %q is not <service>:<action> with letters, digits, * and ?",
				p,
			)
		}

		expr := strings.NewReplacer(`\*`, ".*", `\?`, ".").Replace(regexp.QuoteMeta(p))
		c.patterns = append(c.patterns, regexp.MustCompile("(?i)^"+expr+"$"))
	}

	return c, nil
}

// refusal is why the hub does not apply a fragment: the reason of Applied,
// OverCeiling or Rejected, and its message.
type refusal struct {
	reason, message string
}

// check returns nil when fragment is one IAM statement whose actions the
// ceiling allows. A pattern matches an action as an IAM wildcard, without
// regard to case. An action is read as text, so the pattern s3:* allows the
// action s3:Get*, and the pattern s3:GetObject does not.
func (c Ceiling) check(fragment []byte) *refusal {
	var statement struct {
		Effect    string
		Action    json.RawMessage
		NotAction json.RawMessage
	}
	decoder := json.NewDecoder(bytes.NewReader(fragment))
	if err := decoder.Decode(&statement); err != nil {
		return rejected("The fragment is not one IAM statement: " + err.Error())
	}

	if statement.NotAction != nil {
		return rejected("NotAction is not allowed: the ceiling checks named actions")
	}

	if statement.Effect != "Allow" && statement.Effect != "Deny" {
		return rejected(fmt.Sprintf("The statement has the Effect %q, which is not Allow or Deny", statement.Effect))
	}

	actions, ok := actionList(statement.Action)
	if !ok {
		return rejected("The statement has no Action, or an Action that is not a string or a list of strings")
	}

	for _, a := range actions {
		if !c.allows(a) {
			return &refusal{
				reason:  pluginsv1alpha1.ReasonOverCeiling,
				message: "Action " + a + " is outside the allowed actions of the plugin",
			}
		}
	}

	return nil
}

func rejected(message string) *refusal {
	return &refusal{reason: pluginsv1alpha1.ReasonRejected, message: message}
}

// actionList reads Action as one string or a list of strings. It reports
// false for no action.
func actionList(action json.RawMessage) ([]string, bool) {
	var one string
	if err := json.Unmarshal(action, &one); err == nil {
		return []string{one}, one != ""
	}

	var list []string
	if err := json.Unmarshal(action, &list); err != nil || len(list) == 0 {
		return nil, false
	}
	return list, true
}

func (c Ceiling) allows(action string) bool {
	for _, p := range c.patterns {
		if p.MatchString(action) {
			return true
		}
	}
	return false
}
