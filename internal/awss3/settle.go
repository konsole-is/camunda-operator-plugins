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

// setting is a bucket setting that S3 can show late after a write.
type setting int

const (
	settingPublicAccessBlock setting = iota
	settingEncryption
	settingAbortRule
)

// written is what the plugin knows of one setting of a bucket from its own
// writes.
type written struct {
	// Value is the value of the last write of the setting, when Wrote.
	Value string
	Wrote bool
	// Initial says that the plugin created the bucket, did not write the
	// setting since, and did not finish the bucket yet.
	Initial bool
	// Settling says that S3 can still show the setting as it was before the
	// last write of the plugin.
	Settling bool
}

// writeLog keeps in memory the time of the last write and the written values
// of each bucket. A nil writeLog keeps nothing, and no write settles.
type writeLog struct {
	settle time.Duration
	now    func() time.Time

	mu      sync.Mutex
	buckets map[string]*bucketWrites
}

type bucketWrites struct {
	at time.Time
	// initial says that the plugin created the bucket and did not finish it.
	initial bool
	values  map[setting]string
}

func newWriteLog(settle time.Duration, now func() time.Time) *writeLog {
	return &writeLog{settle: settle, now: now, buckets: map[string]*bucketWrites{}}
}

// left returns how long S3 can still show a setting of the bucket name as it
// was before the last write of the plugin. A process does not know the writes
// of the process before it, so it counts a bucket without a write as written
// at its first look.
func (w *writeLog) left(name string) time.Duration {
	if w == nil {
		return 0
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	return w.leftOf(w.bucket(name))
}

// of returns what the plugin knows of the setting s of the bucket name.
func (w *writeLog) of(name string, s setting) written {
	if w == nil {
		return written{}
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	b, ok := w.buckets[name]
	if !ok {
		return written{}
	}
	value, wrote := b.values[s]
	return written{
		Value:    value,
		Wrote:    wrote,
		Initial:  b.initial && !wrote,
		Settling: wrote && w.leftOf(b) > 0,
	}
}

// created records that the plugin created the bucket name now, so that it
// wrote no setting of it yet.
func (w *writeLog) created(name string) {
	if w == nil {
		return
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	w.buckets[name] = &bucketWrites{at: w.now(), initial: true, values: map[setting]string{}}
}

// finished records that each setting of the bucket name has its desired
// value, as written or as S3 showed it.
func (w *writeLog) finished(name string) {
	if w == nil {
		return
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if b, ok := w.buckets[name]; ok {
		b.initial = false
	}
}

// wrote records a write of the tags of the bucket name now.
func (w *writeLog) wrote(name string) {
	if w == nil {
		return
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	w.bucket(name).at = w.now()
}

// wroteSetting records a write of the value of the setting s on the bucket
// name now.
func (w *writeLog) wroteSetting(name string, s setting, value string) {
	if w == nil {
		return
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	b := w.bucket(name)
	b.at = w.now()
	b.values[s] = value
}

// forget drops the bucket name.
func (w *writeLog) forget(name string) {
	if w == nil {
		return
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.buckets, name)
}

// bucket returns the writes of the bucket name, and adds them at the first
// look. The caller holds the lock.
func (w *writeLog) bucket(name string) *bucketWrites {
	b, ok := w.buckets[name]
	if !ok {
		b = &bucketWrites{at: w.now(), values: map[setting]string{}}
		w.buckets[name] = b
	}
	return b
}

func (w *writeLog) leftOf(b *bucketWrites) time.Duration {
	return max(0, w.settle-w.now().Sub(b.at))
}
