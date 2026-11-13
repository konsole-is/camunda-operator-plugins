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
	"errors"
	"fmt"
	"time"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
)

// ownTags is the number of tags that a bucket carries besides the request
// tags and the extra tags: awskit.TagRequestUID.
const ownTags = 1

// Settings are the settings of the plugin that the chart gives.
type Settings struct {
	// Region is the region of the bucket of a request that sets none.
	Region string
	// Partition is the AWS partition of the buckets, for their ARNs and their
	// console links.
	Partition string
	// Mode is how the pods of a cluster get their AWS role. Only ModeIRSA
	// names the role in the ObjectStorageConfig.
	Mode awskit.Mode
	// ExtraTags go on each bucket, next to the request tags.
	ExtraTags awskit.ExtraTags
	// DeleteBudget is the time for which one pass of a Delete deletes
	// objects. A bucket with more objects takes more passes.
	DeleteBudget time.Duration
}

// Validate returns an error that names the first field that is not valid: an
// empty region, a partition that awskit.CheckPartition refuses, a mode that
// is not irsa or podIdentity, more extra tags than awskit.CheckExtraTags
// allows next to the UID tag, or a delete budget that is not positive.
func (s Settings) Validate() error {
	if s.Region == "" {
		return errors.New("region: empty")
	}

	if err := awskit.CheckPartition(s.Partition); err != nil {
		return fmt.Errorf("partition: %w", err)
	}

	if _, err := awskit.ParseMode(string(s.Mode)); err != nil {
		return fmt.Errorf("mode: %w", err)
	}

	if err := awskit.CheckExtraTags(s.ExtraTags, ownTags); err != nil {
		return fmt.Errorf("extra tags: %w", err)
	}

	if s.DeleteBudget <= 0 {
		return fmt.Errorf("delete budget: %s is not positive", s.DeleteBudget)
	}

	return nil
}
