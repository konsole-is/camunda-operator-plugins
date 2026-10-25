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
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"unicode"
)

// The version of the IAM policy language, and the limit of IAM on the size of
// all inline policies of one role, in characters without white space.
const (
	PolicyVersion       = "2012-10-17"
	MaxInlinePolicySize = 10240
)

// PolicyDocument returns the compact policy document that holds statements,
// in their order. It refuses a statement that is not JSON.
func PolicyDocument(statements ...json.RawMessage) ([]byte, error) {
	doc, err := json.Marshal(struct {
		Version   string
		Statement []json.RawMessage
	}{PolicyVersion, statements})
	if err != nil {
		return nil, fmt.Errorf("encoding the policy document: %w", err)
	}

	var out bytes.Buffer
	if err := json.Compact(&out, doc); err != nil {
		return nil, fmt.Errorf("compacting the policy document: %w", err)
	}

	return out.Bytes(), nil
}

// PolicySize returns the size of doc as IAM counts it against
// MaxInlinePolicySize: the characters that are not white space.
func PolicySize(doc []byte) int {
	size := 0
	for _, r := range string(doc) {
		if !unicode.IsSpace(r) {
			size++
		}
	}
	return size
}

// DecodePolicy returns the policy document encoded, URL-encoded as IAM
// returns the documents of GetRole and GetRolePolicy.
func DecodePolicy(encoded string) ([]byte, error) {
	doc, err := url.QueryUnescape(encoded)
	if err != nil {
		return nil, fmt.Errorf("decoding the policy document: %w", err)
	}
	return []byte(doc), nil
}

// SamePolicy reports whether a and b are the same JSON value. Numbers compare
// by their digits. A document that is not JSON is the same as nothing.
func SamePolicy(a, b []byte) bool {
	va, errA := decodeJSON(a)
	vb, errB := decodeJSON(b)
	return errA == nil && errB == nil && reflect.DeepEqual(va, vb)
}

func decodeJSON(doc []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(doc))
	decoder.UseNumber()

	var v any
	if err := decoder.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}
