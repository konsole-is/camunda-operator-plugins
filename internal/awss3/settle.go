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
	"sync"
	"time"
)

// settleTime is how long S3 can show a bucket setting as it was before a
// write. AWS documents a delay of a few minutes for a new lifecycle
// configuration.
const settleTime = 5 * time.Minute

// writeTimes keeps the time of the last write of the plugin on each bucket,
// in memory. A nil writeTimes keeps nothing, and no write settles.
type writeTimes struct {
	settle time.Duration
	now    func() time.Time

	mu sync.Mutex
	at map[string]time.Time
}

func newWriteTimes(settle time.Duration, now func() time.Time) *writeTimes {
	return &writeTimes{settle: settle, now: now, at: map[string]time.Time{}}
}

// left returns how long S3 can still show a setting of the bucket name as it
// was before the last write of the plugin. A process does not know the writes
// of the process before it, so it counts a bucket without a write as written
// at its first look.
func (w *writeTimes) left(name string) time.Duration {
	if w == nil {
		return 0
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.now()
	at, ok := w.at[name]
	if !ok {
		w.at[name] = now
		at = now
	}
	return max(0, w.settle-now.Sub(at))
}

// wrote records a write of the plugin on the bucket name now.
func (w *writeTimes) wrote(name string) {
	if w == nil {
		return
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	w.at[name] = w.now()
}

// forget drops the bucket name.
func (w *writeTimes) forget(name string) {
	if w == nil {
		return
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.at, name)
}
