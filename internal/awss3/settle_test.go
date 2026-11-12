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

func TestWriteTimesCountAnUnknownBucketAsWrittenAtItsFirstLook(t *testing.T) {
	c := &clock{now: time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)}
	w := newWriteTimes(time.Minute, c.Now)

	first := w.left("b1")
	c.now = c.now.Add(20 * time.Second)

	assert.Equal(t, time.Minute, first)
	assert.Equal(t, 40*time.Second, w.left("b1"))
}

func TestWriteTimesStartTheSettleTimeAgainAtAWrite(t *testing.T) {
	c := &clock{now: time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)}
	w := newWriteTimes(time.Minute, c.Now)
	w.wrote("b1")
	c.now = c.now.Add(2 * time.Minute)
	assert.Zero(t, w.left("b1"))

	w.wrote("b1")
	c.now = c.now.Add(15 * time.Second)

	assert.Equal(t, 45*time.Second, w.left("b1"))
}

func TestWriteTimesForgetABucket(t *testing.T) {
	c := &clock{now: time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)}
	w := newWriteTimes(time.Minute, c.Now)
	w.wrote("b1")
	c.now = c.now.Add(2 * time.Minute)

	w.forget("b1")

	assert.Equal(t, time.Minute, w.left("b1"), "a forgotten bucket is unknown again")
}

func TestNoWriteTimesSettleNothing(t *testing.T) {
	var w *writeTimes
	w.wrote("b1")

	assert.Zero(t, w.left("b1"))
}
