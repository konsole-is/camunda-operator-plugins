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

package awsfake

import (
	"context"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
)

var _ awskit.STS = (*STS)(nil)

// STS is an in-memory STS.
type STS struct {
	// Account is the account of the caller. Empty is DefaultAccount.
	Account string

	mu   sync.Mutex
	fail failures
}

// NewSTS returns an STS of DefaultAccount.
func NewSTS() *STS {
	return &STS{}
}

// FailNext makes the next call of operation return err.
func (f *STS) FailNext(operation string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail.next(operation, err)
}

// GetCallerIdentity returns the account of the fake.
func (f *STS) GetCallerIdentity(
	_ context.Context,
	_ *sts.GetCallerIdentityInput,
	_ ...func(*sts.Options),
) (*sts.GetCallerIdentityOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail.call("GetCallerIdentity"); err != nil {
		return nil, err
	}

	account := f.Account
	if account == "" {
		account = DefaultAccount
	}
	return &sts.GetCallerIdentityOutput{Account: aws.String(account)}, nil
}
