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
	"flag"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequestTagsNameTheNamespaceTheClaimAndTheRequest(t *testing.T) {
	assert.Equal(t, map[string]string{
		"camunda.io/namespace": "team-a",
		"camunda.io/claim":     "orders",
		"camunda.io/request":   "orders-identity",
	}, RequestTags("team-a", "orders", "orders-identity"))
}

func TestExtraTagsParseAListOfKeyValuePairs(t *testing.T) {
	var tags ExtraTags

	require.NoError(t, tags.Set("team=payments, cpo-ci-run=cpo-ci-42"))

	assert.Equal(t, ExtraTags{"team": "payments", "cpo-ci-run": "cpo-ci-42"}, tags)
}

func TestExtraTagsAcceptEachCharacterThatAWSAllows(t *testing.T) {
	var tags ExtraTags

	require.NoError(t, tags.Set("Kostnaður 1_.:/+-@=Á b_.:/=+-@"))

	assert.Equal(t, ExtraTags{"Kostnaður 1_.:/+-@": "Á b_.:/=+-@"}, tags)
}

func TestExtraTagsSplitAtTheFirstEquals(t *testing.T) {
	var tags ExtraTags

	require.NoError(t, tags.Set("code=YWJj==,empty="))

	assert.Equal(t, ExtraTags{"code": "YWJj==", "empty": ""}, tags)
}

func TestExtraTagsAddEachSetToTheOthers(t *testing.T) {
	var tags ExtraTags

	require.NoError(t, tags.Set("a=1"))
	require.NoError(t, tags.Set("b=2"))

	assert.Equal(t, ExtraTags{"a": "1", "b": "2"}, tags)
}

func TestExtraTagsIgnoreAnEmptyList(t *testing.T) {
	var tags ExtraTags

	require.NoError(t, tags.Set(""))
	require.NoError(t, tags.Set(" , "))

	assert.Empty(t, tags)
}

func TestExtraTagsAcceptTheLongestKeyAndValue(t *testing.T) {
	for _, char := range []string{"k", "é"} {
		t.Run(char, func(t *testing.T) {
			key, value := strings.Repeat(char, 128), strings.Repeat(char, 256)
			var tags ExtraTags

			require.NoError(t, tags.Set(key+"="+value))

			assert.Equal(t, ExtraTags{key: value}, tags)
		})
	}
}

func TestExtraTagsRefuseABadItem(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		names string
	}{
		{name: "no equals", in: "novalue", names: "novalue"},
		{name: "good item then a bad one", in: "ok=1,novalue", names: "novalue"},
		{name: "empty key", in: "=v", names: "=v"},
		{name: "AWS key", in: "aws:createdBy=x", names: "aws:createdBy"},
		{name: "AWS key upper case", in: "AWS:x=1", names: "AWS:x"},
		{name: "request key", in: "camunda.io/claim=x", names: "camunda.io/claim"},
		{name: "any camunda.io key", in: "camunda.io/other=x", names: "camunda.io/other"},
		{name: "camunda.io key upper case", in: "CAMUNDA.IO/claim=x", names: "CAMUNDA.IO/claim"},
		{name: "key set before", in: "team=other", names: "team"},
		{name: "key set before in another case", in: "Team=other", names: "Team"},
		{name: "key twice in one list", in: "a=1,a=2", names: "a"},
		{name: "long key", in: strings.Repeat("k", 129) + "=v", names: "128"},
		{name: "long value", in: "k=" + strings.Repeat("v", 257), names: "256"},
		{name: "mark in the key", in: "team!=x", names: "team!"},
		{name: "mark in the value", in: "k=payments!", names: "payments!"},
		{name: "long key of two-byte characters", in: strings.Repeat("é", 129) + "=v", names: "128"},
		{name: "long value of two-byte characters", in: "k=" + strings.Repeat("é", 257), names: "256"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tags := ExtraTags{"team": "payments"}

			require.ErrorContains(t, tags.Set(tt.in), tt.names)
			assert.Equal(t, ExtraTags{"team": "payments"}, tags)
		})
	}
}

func TestExtraTagsRefuseMoreThanTheLimit(t *testing.T) {
	items := make([]string, MaxExtraTags)
	for i := range items {
		items[i] = fmt.Sprintf("k%d=v", i)
	}

	var tags ExtraTags
	require.NoError(t, tags.Set(strings.Join(items, ",")))

	require.ErrorContains(t, tags.Set("one=more"), "47")
	assert.Len(t, tags, 47)
}

func TestExtraTagsStringListsTheTagsSortedByKey(t *testing.T) {
	tags := ExtraTags{"b": "2", "a": "x=y"}

	assert.Equal(t, "a=x=y,b=2", tags.String())
	assert.Empty(t, ExtraTags(nil).String())

	var again ExtraTags
	require.NoError(t, again.Set(tags.String()))
	assert.Equal(t, tags, again)
}

func TestExtraTagsWorkAsAFlag(t *testing.T) {
	var tags ExtraTags
	fs := flag.NewFlagSet("plugin", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Var(&tags, "extra-tags", "")

	require.NoError(t, fs.Parse([]string{"-extra-tags=a=1,b=2", "-extra-tags=c=3"}))
	assert.Equal(t, ExtraTags{"a": "1", "b": "2", "c": "3"}, tags)

	require.Error(t, fs.Parse([]string{"-extra-tags=aws:x=1"}))
	fs.PrintDefaults() // the flag package calls String on a zero ExtraTags
}

func TestExtraTagsRequestTagsAddTheRequestKeys(t *testing.T) {
	tags := ExtraTags{"cpo-ci-run": "cpo-ci-42"}

	got := tags.RequestTags("team-a", "orders", "orders-kms")
	assert.Equal(t, map[string]string{
		"cpo-ci-run":           "cpo-ci-42",
		"camunda.io/namespace": "team-a",
		"camunda.io/claim":     "orders",
		"camunda.io/request":   "orders-kms",
	}, got)

	got["extra"] = "x"
	assert.Equal(t, ExtraTags{"cpo-ci-run": "cpo-ci-42"}, tags)

	assert.Equal(
		t,
		RequestTags("team-a", "orders", "orders-kms"),
		ExtraTags(nil).RequestTags("team-a", "orders", "orders-kms"),
	)
}
