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

package rustfs

import (
	"errors"
	"fmt"
	"net/url"

	v1 "github.com/konsole-is/camunda-operator/api/v1"
)

// Settings are the chart values of the RustFS server that every request uses.
type Settings struct {
	// Endpoint is the S3 endpoint of RustFS, an absolute http or https URL,
	// as the controller and the Camunda pods reach it.
	Endpoint string
	// Region is the region of the S3 API. Empty is v1.PlaceholderS3Region.
	Region string
	// ConsoleURL, when set, is an absolute URL of the RustFS console.
	ConsoleURL     string
	AdminAccessKey string
	AdminSecretKey string
}

// validate returns an error that names each setting that is not valid.
func (s Settings) validate() error {
	var errs []error
	if _, err := parseEndpoint(s.Endpoint); err != nil {
		errs = append(errs, fmt.Errorf("endpoint: %w", err))
	}

	if s.ConsoleURL != "" {
		if u, err := url.Parse(s.ConsoleURL); err != nil || u.Scheme == "" || u.Host == "" {
			errs = append(errs, fmt.Errorf("consoleUrl: %q is not an absolute URL", s.ConsoleURL))
		}
	}

	if s.AdminAccessKey == "" {
		errs = append(errs, errors.New("the admin access key is empty"))
	}

	if s.AdminSecretKey == "" {
		errs = append(errs, errors.New("the admin secret key is empty"))
	}

	return errors.Join(errs...)
}

func (s Settings) region() string {
	if s.Region == "" {
		return v1.PlaceholderS3Region
	}
	return s.Region
}
