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
	"sync"
)

// fakeUser is a RustFS user of fakeServer.
type fakeUser struct {
	secretKey string
	policy    string
}

// fakeServer is an in-memory RustFS: it implements admin and buckets.
type fakeServer struct {
	mu           sync.Mutex
	buckets      map[string]bool
	users        map[string]fakeUser
	policies     map[string][]byte
	addUserCalls int
	failures     map[string]error
}

// counts is the number of buckets, users, and policies of a fakeServer.
type counts struct{ buckets, users, policies int }

func (c counts) plus(n int) counts {
	return counts{buckets: c.buckets + n, users: c.users + n, policies: c.policies + n}
}

func newFakeServer() *fakeServer {
	return &fakeServer{
		buckets:  map[string]bool{},
		users:    map[string]fakeUser{},
		policies: map[string][]byte{},
		failures: map[string]error{},
	}
}

// fail makes each call of method return err until heal.
func (f *fakeServer) fail(method string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures[method] = err
}

func (f *fakeServer) heal(method string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.failures, method)
}

func (f *fakeServer) counts() counts {
	f.mu.Lock()
	defer f.mu.Unlock()
	return counts{buckets: len(f.buckets), users: len(f.users), policies: len(f.policies)}
}

func (f *fakeServer) user(name string) (fakeUser, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[name]
	return u, ok
}

func (f *fakeServer) hasBucket(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.buckets[name]
}

func (f *fakeServer) policy(name string) ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.policies[name]
	return p, ok
}

func (f *fakeServer) addUsers() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.addUserCalls
}

func (f *fakeServer) Ping(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.failures["Ping"]
}

func (f *fakeServer) UserInfo(_ context.Context, accessKey string) (userInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failures["UserInfo"]; err != nil {
		return userInfo{}, err
	}
	u, ok := f.users[accessKey]
	if !ok {
		return userInfo{}, &AdminError{Call: "user-info", Status: 404, Code: "NoSuchResource"}
	}
	return userInfo{PolicyName: u.policy, Status: "enabled"}, nil
}

func (f *fakeServer) AddUser(_ context.Context, accessKey, secretKey string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failures["AddUser"]; err != nil {
		return err
	}
	f.addUserCalls++
	u := f.users[accessKey]
	u.secretKey = secretKey
	f.users[accessKey] = u
	return nil
}

func (f *fakeServer) RemoveUser(_ context.Context, accessKey string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failures["RemoveUser"]; err != nil {
		return err
	}
	if _, ok := f.users[accessKey]; !ok {
		return &AdminError{Call: "remove-user", Status: 404, Code: "NoSuchResource"}
	}
	delete(f.users, accessKey)
	return nil
}

func (f *fakeServer) PolicyExists(_ context.Context, name string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.policies[name]
	return ok, f.failures["PolicyExists"]
}

func (f *fakeServer) AddPolicy(_ context.Context, name string, document []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failures["AddPolicy"]; err != nil {
		return err
	}
	f.policies[name] = document
	return nil
}

func (f *fakeServer) RemovePolicy(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failures["RemovePolicy"]; err != nil {
		return err
	}
	delete(f.policies, name)
	return nil
}

func (f *fakeServer) AttachPolicy(_ context.Context, policy, user string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failures["AttachPolicy"]; err != nil {
		return err
	}
	u := f.users[user]
	u.policy = policy
	f.users[user] = u
	return nil
}

func (f *fakeServer) BucketSizes(context.Context) (map[string]uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failures["BucketSizes"]; err != nil {
		return nil, err
	}
	sizes := map[string]uint64{}
	for b := range f.buckets {
		sizes[b] = 2048
	}
	return sizes, nil
}

func (f *fakeServer) EnsureBucket(_ context.Context, name string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failures["EnsureBucket"]; err != nil {
		return false, err
	}
	created := !f.buckets[name]
	f.buckets[name] = true
	return created, nil
}

func (f *fakeServer) DeleteBucket(_ context.Context, name string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failures["DeleteBucket"]; err != nil {
		return false, err
	}
	existed := f.buckets[name]
	delete(f.buckets, name)
	return existed, nil
}
