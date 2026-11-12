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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestWriteLogCountsAnUnknownBucketAsWrittenAtItsFirstLook(t *testing.T) {
	c := &clock{now: time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)}
	w := newWriteLog(time.Minute, c.Now)

	first := w.left("b1")
	c.now = c.now.Add(20 * time.Second)

	assert.Equal(t, time.Minute, first)
	assert.Equal(t, 40*time.Second, w.left("b1"))
}

func TestWriteLogStartsTheSettleTimeAgainAtAWrite(t *testing.T) {
	c := &clock{now: time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)}
	w := newWriteLog(time.Minute, c.Now)
	w.wrote("b1")
	c.now = c.now.Add(2 * time.Minute)
	assert.Zero(t, w.left("b1"))

	w.wrote("b1")
	c.now = c.now.Add(15 * time.Second)

	assert.Equal(t, 45*time.Second, w.left("b1"))
}

func TestWriteLogForgetsABucket(t *testing.T) {
	c := &clock{now: time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)}
	w := newWriteLog(time.Minute, c.Now)
	w.wrote("b1")
	c.now = c.now.Add(2 * time.Minute)

	w.forget("b1")

	assert.Equal(t, time.Minute, w.left("b1"), "a forgotten bucket is unknown again")
}

func TestNoWriteLogSettlesNothing(t *testing.T) {
	var w *writeLog
	w.created("b1")
	w.wroteSetting("b1", settingEncryption, "")

	assert.Zero(t, w.left("b1"))
	assert.Equal(t, written{}, w.of("b1", settingEncryption))
}

func TestWriteLogKnowsNoSettingOfABucketThatItCreated(t *testing.T) {
	c := &clock{now: time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)}
	w := newWriteLog(time.Minute, c.Now)
	w.wroteSetting("b1", settingEncryption, "key-a")

	w.created("b1")

	assert.Equal(t, written{Initial: true}, w.of("b1", settingEncryption), "a new bucket has no write of before")
}

func TestWriteLogKeepsTheLastValueOfASetting(t *testing.T) {
	c := &clock{now: time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)}
	w := newWriteLog(time.Minute, c.Now)
	w.created("b1")
	w.wroteSetting("b1", settingEncryption, "key-a")
	w.wroteSetting("b1", settingEncryption, "key-b")

	settling := w.of("b1", settingEncryption)
	c.now = c.now.Add(time.Minute)

	assert.Equal(t, written{Value: "key-b", Wrote: true, Settling: true}, settling)
	assert.Equal(t, written{Value: "key-b", Wrote: true}, w.of("b1", settingEncryption))
	assert.Equal(t, written{Initial: true}, w.of("b1", settingAbortRule))
}

func TestWriteLogKnowsNoSettingAfterAFirstLook(t *testing.T) {
	c := &clock{now: time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)}
	w := newWriteLog(time.Minute, c.Now)

	w.left("b1")

	assert.Equal(t, written{}, w.of("b1", settingEncryption), "a process knows no write of the process before")
}

func TestWriteLogEndsTheInitialStateOfAFinishedBucket(t *testing.T) {
	c := &clock{now: time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)}
	w := newWriteLog(time.Minute, c.Now)
	w.created("b1")

	w.finished("b1")

	assert.Equal(t, written{}, w.of("b1", settingPublicAccessBlock))
}
