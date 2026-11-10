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
	"errors"
	"fmt"
	"testing"

	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
)

func TestErrorsMatchTheCodesOfEachService(t *testing.T) {
	checks := map[string]func(error) bool{
		"IsNotFound":        IsNotFound,
		"IsAlreadyExists":   IsAlreadyExists,
		"IsLimitExceeded":   IsLimitExceeded,
		"IsMalformedPolicy": IsMalformedPolicy,
		"IsAccessDenied":    IsAccessDenied,
		"IsNotConfigured":   IsNotConfigured,
	}
	codes := map[string][]string{
		"IsNotFound": {
			"NoSuchEntity",
			"NoSuchEntityException",
			"ResourceNotFoundException",
			"NotFoundException",
			"NoSuchBucket",
			"NotFound",
		},
		"IsAlreadyExists":   {"EntityAlreadyExists", "ResourceInUseException", "AlreadyExistsException"},
		"IsLimitExceeded":   {"LimitExceeded", "LimitExceededException"},
		"IsMalformedPolicy": {"MalformedPolicyDocument", "MalformedPolicyDocumentException"},
		"IsAccessDenied":    {"AccessDenied", "AccessDeniedException", "Forbidden"},
		"IsNotConfigured": {
			"NoSuchTagSet",
			"NoSuchPublicAccessBlockConfiguration",
			"ServerSideEncryptionConfigurationNotFoundError",
			"NoSuchLifecycleConfiguration",
		},
	}

	for name, check := range checks {
		for owner, list := range codes {
			for _, code := range list {
				err := fmt.Errorf("calling AWS: %w", &smithy.GenericAPIError{Code: code, Message: "m"})

				assert.Equal(t, name == owner, check(err), "%s(%s)", name, code)
			}
		}
		assert.False(t, check(errors.New("NoSuchEntity")), name)
		assert.False(t, check(nil), name)
	}
}

func TestIsNotFoundMatchesTheS3Codes(t *testing.T) {
	for _, err := range []error{&s3types.NotFound{}, &s3types.NoSuchBucket{}} {
		assert.True(t, IsNotFound(fmt.Errorf("wrapped: %w", err)), "%T", err)
	}
}

func TestErrorCodeAndMessageReadAnAPIError(t *testing.T) {
	err := fmt.Errorf("x: %w", &smithy.GenericAPIError{Code: "DeleteConflict", Message: "the role has policies"})

	assert.Equal(t, "DeleteConflict", ErrorCode(err))
	assert.Equal(t, "the role has policies", Message(err))
	assert.Empty(t, ErrorCode(errors.New("plain")))
	assert.Equal(t, "plain", Message(errors.New("plain")))
}
