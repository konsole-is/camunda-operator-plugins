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
	"encoding/json"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPolicyDocumentHoldsEachStatement(t *testing.T) {
	doc, err := PolicyDocument(json.RawMessage(`{"Effect":"Allow"}`), json.RawMessage(`{ "Effect": "Deny" }`))

	require.NoError(t, err)
	assert.JSONEq(t, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow"},{"Effect":"Deny"}]}`, string(doc))
	assert.NotContains(t, string(doc), " ")
}

func TestPolicyDocumentRefusesAStatementThatIsNotJSON(t *testing.T) {
	_, err := PolicyDocument(json.RawMessage(`{`))

	assert.Error(t, err)
}

func TestPolicySizeDoesNotCountWhiteSpace(t *testing.T) {
	assert.Equal(t, 9, PolicySize([]byte(`{"a":"b"}`)))
	assert.Equal(t, PolicySize([]byte(`{"a":"b"}`)), PolicySize([]byte("{ \"a\" :\n\t\"b\" }")))
}

func TestDecodePolicyReadsTheEncodingOfIAM(t *testing.T) {
	doc, err := DecodePolicy(url.QueryEscape(`{"Version":"2012-10-17","Statement":[{"Resource":"a b+c"}]}`))

	require.NoError(t, err)
	assert.JSONEq(t, `{"Version":"2012-10-17","Statement":[{"Resource":"a b+c"}]}`, string(doc))
}

func TestDecodePolicyRefusesABadEncoding(t *testing.T) {
	_, err := DecodePolicy("%zz")

	assert.Error(t, err)
}

func TestSamePolicyComparesJSONValues(t *testing.T) {
	assert.True(t, SamePolicy([]byte(`{"a":1,"b":[2]}`), []byte(`{ "b":[2], "a":1 }`)))
	assert.True(t, SamePolicy([]byte(`{"n":12345678901234567890}`), []byte(`{"n":12345678901234567890}`)))
	assert.False(t, SamePolicy([]byte(`{"n":12345678901234567890}`), []byte(`{"n":12345678901234567891}`)))
	assert.False(t, SamePolicy([]byte(`{"a":["x","y"]}`), []byte(`{"a":["y","x"]}`)))
	assert.False(t, SamePolicy([]byte(`{"a":1}`), []byte(`not json`)))
	assert.False(t, SamePolicy(nil, []byte(`{}`)))
}

// IAM stores an array of one item as the item, as in the trust policy of a
// role with one sub value.
func TestSamePolicyTakesAnArrayOfOneItemAsTheItem(t *testing.T) {
	assert.True(t, SamePolicy(
		[]byte(`{"Statement":[{"Condition":{"StringEquals":{"sub":["a"]}},"Action":["s3:GetObject"]}]}`),
		[]byte(`{"Statement":{"Condition":{"StringEquals":{"sub":"a"}},"Action":"s3:GetObject"}}`),
	))
	assert.False(t, SamePolicy([]byte(`{"sub":["a","b"]}`), []byte(`{"sub":"a"}`)))
	assert.False(t, SamePolicy([]byte(`{"sub":[]}`), []byte(`{"sub":""}`)))
}
