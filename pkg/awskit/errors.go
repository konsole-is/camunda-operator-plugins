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
	"slices"

	"github.com/aws/smithy-go"
)

// ErrorCode returns the code of the AWS API error in the chain of err, or ""
// when the chain holds none.
func ErrorCode(err error) string {
	if apiErr, ok := errors.AsType[smithy.APIError](err); ok {
		return apiErr.ErrorCode()
	}
	return ""
}

// Message returns the message of the AWS API error in the chain of err, or
// err.Error() when the chain holds none.
func Message(err error) string {
	if apiErr, ok := errors.AsType[smithy.APIError](err); ok {
		return apiErr.ErrorMessage()
	}
	return err.Error()
}

// IsNotFound reports whether err says that the object does not exist, in IAM,
// EKS, or KMS.
func IsNotFound(err error) bool {
	return hasCode(err, "NoSuchEntity", "NoSuchEntityException", "ResourceNotFoundException", "NotFoundException")
}

// IsAlreadyExists reports whether err says that the object exists already.
func IsAlreadyExists(err error) bool {
	return hasCode(err, "EntityAlreadyExists", "ResourceInUseException", "AlreadyExistsException")
}

// IsLimitExceeded reports whether err says that a quota or a size limit
// refused the call, such as the size of the inline policies of a role.
func IsLimitExceeded(err error) bool {
	return hasCode(err, "LimitExceeded", "LimitExceededException")
}

// IsMalformedPolicy reports whether err says that AWS refused a policy
// document.
func IsMalformedPolicy(err error) bool {
	return hasCode(err, "MalformedPolicyDocument", "MalformedPolicyDocumentException")
}

// IsAccessDenied reports whether err says that the caller may not make the
// call.
func IsAccessDenied(err error) bool {
	return hasCode(err, "AccessDenied", "AccessDeniedException")
}

func hasCode(err error, codes ...string) bool {
	code := ErrorCode(err)
	return code != "" && slices.Contains(codes, code)
}
