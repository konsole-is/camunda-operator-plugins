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

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeValues(t *testing.T, content string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "values.yaml")
	require.NoError(t, os.WriteFile(file, []byte(content), 0o600))

	return file
}

func TestReadRegistrationsKeepsTheTopLevelList(t *testing.T) {
	got, err := readRegistrations([]string{writeValues(t, "registrations: [{name: rustfs-bucket}]\n")})

	require.NoError(t, err)
	assert.Equal(t, []string{"rustfs-bucket"}, got)
}

func TestReadRegistrationsTakesTheBlocksThatAreOn(t *testing.T) {
	base := writeValues(t, "identity: {enabled: false, registrations: [{name: aws-irsa}]}\n"+
		"kms: {enabled: false, registrations: [{name: aws-kms-key}, {name: aws-kms-key-multi-region}]}\n")
	on := writeValues(t, "kms: {enabled: true}\n")

	got, err := readRegistrations([]string{base, on})

	require.NoError(t, err)
	assert.Equal(t, []string{"aws-kms-key", "aws-kms-key-multi-region"}, got)
}

func TestReadRegistrationsTakesTheBlocksInKeyOrder(t *testing.T) {
	values := writeValues(t, "s3: {enabled: true, registrations: [{name: aws-s3}]}\n"+
		"ebs: {enabled: true, registrations: [{name: aws-ebs-encrypted}]}\n")

	got, err := readRegistrations([]string{values})

	require.NoError(t, err)
	assert.Equal(t, []string{"aws-ebs-encrypted", "aws-s3"}, got)
}

func TestReadRegistrationsReplacesAListOfAnEarlierFile(t *testing.T) {
	base := writeValues(t, "kms: {enabled: true, registrations: [{name: aws-kms-key}]}\n")
	renamed := writeValues(t, "kms: {registrations: [{name: skeleton-public}, {name: skeleton-internal}]}\n")

	got, err := readRegistrations([]string{base, renamed})

	require.NoError(t, err)
	assert.Equal(t, []string{"skeleton-public", "skeleton-internal"}, got)
}

func TestReadRegistrationsReadsAnchorsOfTheValues(t *testing.T) {
	values := writeValues(t, "kms:\n  enabled: true\n  registrations:\n"+
		"    - {name: aws-kms-key, outputs: &outputs [{key: keyRef}]}\n"+
		"    - {name: aws-kms-key-multi-region, outputs: *outputs}\n")

	got, err := readRegistrations([]string{values})

	require.NoError(t, err)
	assert.Equal(t, []string{"aws-kms-key", "aws-kms-key-multi-region"}, got)
}

func TestReadRegistrationsNamesTheFileThatDoesNotDecode(t *testing.T) {
	broken := writeValues(t, "registrations: [\n")

	_, err := readRegistrations([]string{broken})

	require.Error(t, err)
	assert.Contains(t, err.Error(), broken)
}
