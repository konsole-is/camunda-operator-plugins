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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

// ErrNotFound means that RustFS has no user or policy of the name.
var ErrNotFound = errors.New("not found")

// AdminError is an answer of the RustFS admin API other than 2xx.
type AdminError struct {
	// Call is the admin call, for example add-user.
	Call   string
	Status int
	// Code is the S3 error code of the answer. It is empty when the answer
	// has no S3 error.
	Code    string
	Message string
}

// Error names the call, and the S3 error of the answer or else its status.
func (e *AdminError) Error() string {
	switch {
	case e.Code != "" && e.Message != "":
		return fmt.Sprintf("RustFS refused %s: %s: %s", e.Call, e.Code, e.Message)
	case e.Code != "":
		return fmt.Sprintf("RustFS refused %s: %s", e.Call, e.Code)
	default:
		return fmt.Sprintf("RustFS answered %s with %d %s", e.Call, e.Status, http.StatusText(e.Status))
	}
}

// Is reports a 404 answer as ErrNotFound.
func (e *AdminError) Is(target error) bool {
	return target == ErrNotFound && e.Status == http.StatusNotFound
}

// userInfo is the part of an answer of user-info that the plugin reads.
type userInfo struct {
	PolicyName string `json:"policyName"`
	Status     string `json:"status"`
}

// adminPath is the prefix of the RustFS admin API whose bodies are plain
// JSON. The MinIO prefix /minio/admin/v3 encrypts them.
const adminPath = "rustfs/admin/v3"

// callTimeout bounds one call of RustFS, of the admin API or of the S3 API.
const callTimeout = 30 * time.Second

// adminClient calls the RustFS admin API with an admin access key.
type adminClient struct {
	endpoint *url.URL
	region   string
	creds    aws.Credentials
	http     *http.Client
	signer   *v4.Signer
}

// newAdminClient returns a client of the RustFS server at endpoint, an
// absolute http or https URL.
func newAdminClient(endpoint, region, accessKey, secretKey string) (*adminClient, error) {
	u, err := parseEndpoint(endpoint)
	if err != nil {
		return nil, err
	}

	return &adminClient{
		endpoint: u,
		region:   region,
		creds:    aws.Credentials{AccessKeyID: accessKey, SecretAccessKey: secretKey},
		http:     &http.Client{Timeout: callTimeout},
		signer:   v4.NewSigner(),
	}, nil
}

// parseEndpoint returns endpoint as a URL, or an error that names it when it
// is not an absolute http or https URL.
func parseEndpoint(endpoint string) (*url.URL, error) {
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("the RustFS endpoint %q is not an absolute http or https URL", endpoint)
	}
	return u, nil
}

// Ping checks that RustFS answers and accepts the admin key.
func (c *adminClient) Ping(ctx context.Context) error {
	_, err := c.do(ctx, http.MethodGet, "list-canned-policies", nil, nil)
	return err
}

// UserInfo returns the user accessKey, or an error that is ErrNotFound when
// RustFS has no such user.
func (c *adminClient) UserInfo(ctx context.Context, accessKey string) (userInfo, error) {
	raw, err := c.do(ctx, http.MethodGet, "user-info", url.Values{"accessKey": {accessKey}}, nil)
	if err != nil {
		return userInfo{}, err
	}

	var info userInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		return userInfo{}, fmt.Errorf("reading the answer of user-info: %w", err)
	}
	return info, nil
}

// AddUser creates the enabled user accessKey, or sets its secret key when it
// exists.
func (c *adminClient) AddUser(ctx context.Context, accessKey, secretKey string) error {
	// A user without a status is disabled.
	body, _ := json.Marshal(struct {
		SecretKey string `json:"secretKey"`
		Status    string `json:"status"`
	}{SecretKey: secretKey, Status: "enabled"})

	_, err := c.do(ctx, http.MethodPut, "add-user", url.Values{"accessKey": {accessKey}}, body)
	return err
}

// RemoveUser deletes the user accessKey.
func (c *adminClient) RemoveUser(ctx context.Context, accessKey string) error {
	_, err := c.do(ctx, http.MethodDelete, "remove-user", url.Values{"accessKey": {accessKey}}, nil)
	return err
}

// PolicyExists reports whether RustFS has the policy name.
func (c *adminClient) PolicyExists(ctx context.Context, name string) (bool, error) {
	_, err := c.do(ctx, http.MethodGet, "info-canned-policy", url.Values{"name": {name}}, nil)
	switch {
	case errors.Is(err, ErrNotFound):
		return false, nil
	case err != nil:
		return false, err
	}
	return true, nil
}

// AddPolicy creates the policy name with document, or replaces it.
func (c *adminClient) AddPolicy(ctx context.Context, name string, document []byte) error {
	_, err := c.do(ctx, http.MethodPut, "add-canned-policy", url.Values{"name": {name}}, document)
	return err
}

// RemovePolicy deletes the policy name.
func (c *adminClient) RemovePolicy(ctx context.Context, name string) error {
	_, err := c.do(ctx, http.MethodDelete, "remove-canned-policy", url.Values{"name": {name}}, nil)
	return err
}

// AttachPolicy makes policy the policy of the user.
func (c *adminClient) AttachPolicy(ctx context.Context, policy, user string) error {
	query := url.Values{"policyName": {policy}, "userOrGroup": {user}, "isGroup": {"false"}}
	_, err := c.do(ctx, http.MethodPut, "set-user-or-group-policy", query, nil)
	return err
}

// BucketSizes returns the used size in bytes of each bucket that the RustFS
// scanner counted. A new bucket is missing until the scanner counts it.
func (c *adminClient) BucketSizes(ctx context.Context) (map[string]uint64, error) {
	raw, err := c.do(ctx, http.MethodGet, "datausageinfo", nil, nil)
	if err != nil {
		return nil, err
	}

	var usage struct {
		Buckets map[string]struct {
			Size uint64 `json:"size"`
		} `json:"buckets_usage"`
	}
	if err := json.Unmarshal(raw, &usage); err != nil {
		return nil, fmt.Errorf("reading the answer of datausageinfo: %w", err)
	}

	sizes := make(map[string]uint64, len(usage.Buckets))
	for name, b := range usage.Buckets {
		sizes[name] = b.Size
	}
	return sizes, nil
}

// do sends one signed call of the admin API and returns the body of a 2xx
// answer. Any other answer is an *AdminError.
func (c *adminClient) do(
	ctx context.Context,
	method, call string,
	query url.Values,
	body []byte,
) ([]byte, error) {
	u := c.endpoint.JoinPath(adminPath, call)
	u.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("building the RustFS admin call %s: %w", call, err)
	}

	sum := sha256.Sum256(body)
	payloadHash := hex.EncodeToString(sum[:])
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	if err := c.signer.SignHTTP(ctx, c.creds, req, payloadHash, "s3", c.region, time.Now()); err != nil {
		return nil, fmt.Errorf("signing the RustFS admin call %s: %w", call, err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling the RustFS admin API %s: %w", call, err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading the answer of %s: %w", call, err)
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		adminErr := &AdminError{Call: call, Status: resp.StatusCode}
		var s3Err struct {
			Code    string `xml:"Code"`
			Message string `xml:"Message"`
		}
		if xml.Unmarshal(raw, &s3Err) == nil {
			adminErr.Code, adminErr.Message = s3Err.Code, s3Err.Message
		}
		return nil, adminErr
	}

	return raw, nil
}
