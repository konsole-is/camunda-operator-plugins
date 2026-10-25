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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recorded is one request that the test server received.
type recorded struct {
	method        string
	path          string
	query         url.Values
	body          []byte
	authorization string
	contentHash   string
}

// adminServer answers each request with status and body, and records it.
func adminServer(t *testing.T, status int, body string) (*adminClient, *[]recorded) {
	t.Helper()
	var got []recorded
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		got = append(got, recorded{
			method:        r.Method,
			path:          r.URL.Path,
			query:         r.URL.Query(),
			body:          raw,
			authorization: r.Header.Get("Authorization"),
			contentHash:   r.Header.Get("X-Amz-Content-Sha256"),
		})
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	c, err := newAdminClient(server.URL, "us-east-1", "admin-key", "admin-secret")
	require.NoError(t, err)
	return c, &got
}

func TestAdminClientSendsEachCall(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		answer string
		call   func(c *adminClient) error
		method string
		path   string
		query  url.Values
		body   string
	}{
		{
			name:   "AddUser",
			call:   func(c *adminClient) error { return c.AddUser(ctx, "u1", "s3cr3t-key") },
			method: http.MethodPut,
			path:   "add-user",
			query:  url.Values{"accessKey": {"u1"}},
			body:   `{"secretKey":"s3cr3t-key","status":"enabled"}`,
		},
		{
			name:   "UserInfo",
			answer: `{"policyName":"p1","status":"enabled"}`,
			call: func(c *adminClient) error {
				info, err := c.UserInfo(ctx, "u1")
				assert.Equal(t, userInfo{PolicyName: "p1", Status: "enabled"}, info)
				return err
			},
			method: http.MethodGet,
			path:   "user-info",
			query:  url.Values{"accessKey": {"u1"}},
		},
		{
			name:   "RemoveUser",
			call:   func(c *adminClient) error { return c.RemoveUser(ctx, "u1") },
			method: http.MethodDelete,
			path:   "remove-user",
			query:  url.Values{"accessKey": {"u1"}},
		},
		{
			name:   "AddPolicy",
			call:   func(c *adminClient) error { return c.AddPolicy(ctx, "p1", userPolicy("b1")) },
			method: http.MethodPut,
			path:   "add-canned-policy",
			query:  url.Values{"name": {"p1"}},
			body:   string(userPolicy("b1")),
		},
		{
			name: "PolicyExists",
			call: func(c *adminClient) error {
				exists, err := c.PolicyExists(ctx, "p1")
				assert.True(t, exists)
				return err
			},
			method: http.MethodGet,
			path:   "info-canned-policy",
			query:  url.Values{"name": {"p1"}},
		},
		{
			name:   "RemovePolicy",
			call:   func(c *adminClient) error { return c.RemovePolicy(ctx, "p1") },
			method: http.MethodDelete,
			path:   "remove-canned-policy",
			query:  url.Values{"name": {"p1"}},
		},
		{
			name:   "AttachPolicy",
			call:   func(c *adminClient) error { return c.AttachPolicy(ctx, "p1", "u1") },
			method: http.MethodPut,
			path:   "set-user-or-group-policy",
			query:  url.Values{"policyName": {"p1"}, "userOrGroup": {"u1"}, "isGroup": {"false"}},
		},
		{
			name:   "BucketSizes",
			answer: `{"buckets_usage":{"b1":{"size":2048}}}`,
			call: func(c *adminClient) error {
				sizes, err := c.BucketSizes(ctx)
				assert.Equal(t, map[string]uint64{"b1": 2048}, sizes)
				return err
			},
			method: http.MethodGet,
			path:   "datausageinfo",
			query:  url.Values{},
		},
		{
			name:   "Ping",
			call:   func(c *adminClient) error { return c.Ping(ctx) },
			method: http.MethodGet,
			path:   "list-canned-policies",
			query:  url.Values{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, got := adminServer(t, http.StatusOK, tc.answer)

			require.NoError(t, tc.call(c))

			require.Len(t, *got, 1)
			r := (*got)[0]
			assert.Equal(t, tc.method, r.method)
			assert.Equal(t, "/rustfs/admin/v3/"+tc.path, r.path)
			assert.Equal(t, tc.query, r.query)
			assert.Equal(t, tc.body, string(r.body))
			assert.True(
				t,
				strings.HasPrefix(r.authorization, "AWS4-HMAC-SHA256 Credential=admin-key/"),
				r.authorization,
			)
			assert.Contains(t, r.authorization, "/s3/aws4_request")
			sum := sha256.Sum256(r.body)
			assert.Equal(t, hex.EncodeToString(sum[:]), r.contentHash)
		})
	}
}

func TestAdminClientMapsErrors(t *testing.T) {
	ctx := context.Background()

	t.Run("a missing user is ErrNotFound", func(t *testing.T) {
		c, _ := adminServer(
			t,
			http.StatusNotFound,
			`<Error><Code>NoSuchResource</Code><Message>user not found</Message></Error>`,
		)
		_, err := c.UserInfo(ctx, "u1")
		assert.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("a missing policy does not exist", func(t *testing.T) {
		c, _ := adminServer(t, http.StatusNotFound, "")
		exists, err := c.PolicyExists(ctx, "p1")
		require.NoError(t, err)
		assert.False(t, exists)
	})

	t.Run("a refusal names the call and the S3 error", func(t *testing.T) {
		c, _ := adminServer(
			t,
			http.StatusForbidden,
			`<Error><Code>AccessDenied</Code><Message>Access Denied.</Message></Error>`,
		)
		err := c.AddUser(ctx, "u1", "s3cr3t-key")
		require.EqualError(t, err, "RustFS refused add-user: AccessDenied: Access Denied.")
		assert.NotErrorIs(t, err, ErrNotFound)
	})

	t.Run("an answer without an S3 error names the status", func(t *testing.T) {
		c, _ := adminServer(t, http.StatusInternalServerError, "boom")
		err := c.AddPolicy(ctx, "p1", userPolicy("b1"))
		assert.EqualError(t, err, "RustFS answered add-canned-policy with 500 Internal Server Error")
	})

	t.Run("a server that does not answer names the call", func(t *testing.T) {
		server := httptest.NewServer(http.NotFoundHandler())
		server.Close()
		c, err := newAdminClient(server.URL, "us-east-1", "admin-key", "admin-secret")
		require.NoError(t, err)

		err = c.AddUser(ctx, "u1", "s3cr3t-key")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "add-user")
		var adminErr *AdminError
		assert.False(t, errors.As(err, &adminErr), "no answer is not an answer of the admin API")
	})
}

func TestNewAdminClientRefusesABadEndpoint(t *testing.T) {
	for _, endpoint := range []string{"rustfs:9000", ""} {
		_, err := newAdminClient(endpoint, "us-east-1", "admin-key", "admin-secret")
		require.Error(t, err, endpoint)
		assert.Contains(t, err.Error(), `"`+endpoint+`"`)
	}
}
