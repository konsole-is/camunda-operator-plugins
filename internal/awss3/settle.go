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

// written is what the plugin knows of one setting of a bucket.
type written struct {
	// Value is the value that the plugin last wrote or trusted, when Known.
	Value string
	Known bool
	// Initial says that the plugin created the bucket and knows no value of
	// the setting yet.
	Initial bool
	// Left is how long S3 can still show the setting as it was before the
	// last write of the plugin.
	Left time.Duration
}

// writeLog keeps in memory the times of the writes of the plugin and the
// values of the settings. A nil writeLog keeps nothing, and nothing settles.
type writeLog struct {
	settle time.Duration
	now    func() time.Time

	mu      sync.Mutex
	buckets map[string]*bucketWrites
}

type bucketWrites struct {
	at       time.Time
	created  bool
	settings map[setting]settingWrite
}

// settingWrite is a value of a setting and the time of its write. A zero time
// is a value that the plugin trusted from a read.
type settingWrite struct {
	value string
	at    time.Time
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
	return w.leftAfter(w.bucket(name).at)
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
	sw, known := b.settings[s]
	out := written{Value: sw.value, Known: known, Initial: b.created && !known}
	if known && !sw.at.IsZero() {
		out.Left = w.leftAfter(sw.at)
	}
	return out
}

// created records that the plugin created the bucket name now, so that it
// knows no value of its settings yet.
func (w *writeLog) created(name string) {
	if w == nil {
		return
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	w.buckets[name] = &bucketWrites{at: w.now(), created: true, settings: map[setting]settingWrite{}}
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
	b.settings[s] = settingWrite{value: value, at: b.at}
}

// trusted records value as the value of the setting s of the bucket name,
// from a read that no write of the plugin can make late. It keeps a value
// that the log knows.
func (w *writeLog) trusted(name string, s setting, value string) {
	if w == nil {
		return
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	b := w.bucket(name)
	if _, ok := b.settings[s]; !ok {
		b.settings[s] = settingWrite{value: value}
	}
}

// unsure drops the value of the setting s of the bucket name, after a write
// of it that can have changed the setting and answered with an error.
func (w *writeLog) unsure(name string, s setting) {
	if w == nil {
		return
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if b, ok := w.buckets[name]; ok {
		delete(b.settings, s)
	}
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
		b = &bucketWrites{at: w.now(), settings: map[setting]settingWrite{}}
		w.buckets[name] = b
	}
	return b
}

func (w *writeLog) leftAfter(at time.Time) time.Duration {
	return max(0, w.settle-w.now().Sub(at))
}
