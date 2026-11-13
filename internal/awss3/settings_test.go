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

package awss3

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
)

// chartDefaults are the settings of the default values of the chart, with a
// region.
func chartDefaults() Settings {
	return Settings{
		Region:       "eu-west-1",
		Partition:    "aws",
		Mode:         awskit.ModeIRSA,
		ExtraTags:    awskit.ExtraTags{},
		DeleteBudget: 15 * time.Second,
	}
}

func TestValidateAcceptsTheChartDefaults(t *testing.T) {
	assert.NoError(t, chartDefaults().Validate())
}

func TestValidateRefusesAnEmptyRegion(t *testing.T) {
	s := chartDefaults()
	s.Region = ""

	assert.ErrorContains(t, s.Validate(), "region")
}

func TestValidateRefusesAnEmptyPartition(t *testing.T) {
	s := chartDefaults()
	s.Partition = ""

	assert.ErrorContains(t, s.Validate(), "partition")
}

func TestValidateRefusesAnUnknownPartition(t *testing.T) {
	s := chartDefaults()
	s.Partition = "aws-gov"

	assert.ErrorContains(t, s.Validate(), `partition: the partition "aws-gov"`)
}

func TestValidateRefusesAnUnknownMode(t *testing.T) {
	s := chartDefaults()
	s.Mode = "irsa2"

	assert.ErrorContains(t, s.Validate(), `"irsa2"`)
}

func TestValidateRefusesTooManyExtraTags(t *testing.T) {
	s := chartDefaults()
	for i := range awskit.MaxExtraTags {
		s.ExtraTags[fmt.Sprintf("k%d", i)] = "v"
	}

	assert.ErrorContains(t, s.Validate(), "extra tags")

	delete(s.ExtraTags, "k0")
	assert.NoError(t, s.Validate(), "the UID tag is the one own tag of a bucket")
}

func TestValidateRefusesADeleteBudgetThatIsNotPositive(t *testing.T) {
	s := chartDefaults()
	s.DeleteBudget = 0

	assert.ErrorContains(t, s.Validate(), "delete budget")
}
