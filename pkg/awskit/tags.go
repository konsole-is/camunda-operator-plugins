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

package awskit

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// The tags that tie an AWS object to the request that it serves.
const (
	TagNamespace = "camunda.io/namespace"
	TagClaim     = "camunda.io/claim"
	TagRequest   = "camunda.io/request"
)

// MaxExtraTags is the most extra tags that ExtraTags holds: the 50 tags of
// one AWS object, less the three request tags.
const MaxExtraTags = 47

var tagChars = regexp.MustCompile(`^[\p{L}\p{Z}\p{N}_.:/=+\-@]*$`)

// ExtraTags are the tags that an admin adds to every AWS object of a plugin,
// such as a cost center or the run tag of a sandbox. It implements
// flag.Value.
type ExtraTags map[string]string

// RequestTags returns the tags of an AWS object of a request.
func RequestTags(namespace, claim, request string) map[string]string {
	return map[string]string{
		TagNamespace: namespace,
		TagClaim:     claim,
		TagRequest:   request,
	}
}

// Set adds a comma-separated list of key=value items. The key ends at the
// first "=". Empty items are skipped. Set refuses an item without "=", an
// empty key, a key of more than 128 or a value of more than 256 characters,
// a character that AWS does not allow in a tag, a key that starts with "aws:"
// or "camunda.io/" in any case, a key that is already set in any case, and
// more than MaxExtraTags tags in all. The error names the item, or the whole
// input when there are too many tags. A refused Set changes nothing.
func (t *ExtraTags) Set(s string) error {
	next := maps.Clone(*t)
	if next == nil {
		next = ExtraTags{}
	}

	// IAM users and roles do not allow two tag keys that differ only in case.
	keys := map[string]bool{}
	for key := range next {
		keys[strings.ToLower(key)] = true
	}

	for item := range strings.SplitSeq(s, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}

		key, value, ok := strings.Cut(item, "=")
		lower := strings.ToLower(key)
		switch {
		case !ok:
			return fmt.Errorf("extra tag %q: want key=value", item)
		case key == "":
			return fmt.Errorf("extra tag %q: the key is empty", item)
		case utf8.RuneCountInString(key) > 128:
			return fmt.Errorf("extra tag %q: the key has more than 128 characters", item)
		case utf8.RuneCountInString(value) > 256:
			return fmt.Errorf("extra tag %q: the value has more than 256 characters", item)
		case !tagChars.MatchString(key) || !tagChars.MatchString(value):
			return fmt.Errorf("extra tag %q: AWS allows only letters, digits, spaces, and _.:/=+-@", item)
		case strings.HasPrefix(lower, "aws:"):
			return fmt.Errorf("extra tag %q: the key prefix aws: is reserved by AWS", item)
		case strings.HasPrefix(lower, "camunda.io/"):
			return fmt.Errorf("extra tag %q: the plugin owns the keys of camunda.io/", item)
		case keys[lower]:
			return fmt.Errorf("extra tag %q: the key %q is already set", item, key)
		}

		keys[lower] = true
		next[key] = value
	}

	if len(next) > MaxExtraTags {
		return fmt.Errorf("extra tags %q: more than %d tags in all", s, MaxExtraTags)
	}

	*t = next
	return nil
}

// String returns the tags as key=value items, sorted by key and joined by
// ",". Set reads this form back.
func (t ExtraTags) String() string {
	items := make([]string, 0, len(t))
	for _, key := range slices.Sorted(maps.Keys(t)) {
		items = append(items, key+"="+t[key])
	}

	return strings.Join(items, ",")
}

// RequestTags returns a new map with the extra tags and the tags of
// RequestTags.
func (t ExtraTags) RequestTags(namespace, claim, request string) map[string]string {
	tags := RequestTags(namespace, claim, request)
	maps.Copy(tags, t)

	return tags
}
