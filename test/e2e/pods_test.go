//go:build e2e

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

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// runPod runs pod to its end and returns the logs of its first container.
// The caller sets GenerateName. The pod never restarts, and it is deleted
// before runPod returns. A pod that fails, or that does not end within
// timeout, is an error that holds its logs.
func runPod(pod *corev1.Pod, timeout time.Duration) (string, error) {
	pod.Spec.RestartPolicy = corev1.RestartPolicyNever
	if err := k8s.Create(ctx, pod); err != nil {
		return "", fmt.Errorf("creating the pod %s in %s: %w", pod.GenerateName, pod.Namespace, err)
	}
	defer func() { _ = k8s.Delete(ctx, pod) }()

	var phase corev1.PodPhase
	waitErr := wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		if err := k8s.Get(ctx, client.ObjectKeyFromObject(pod), pod); err != nil {
			return false, err
		}
		phase = pod.Status.Phase
		return phase == corev1.PodSucceeded || phase == corev1.PodFailed, nil
	})

	logs, logErr := clientset.CoreV1().Pods(pod.Namespace).
		GetLogs(pod.Name, &corev1.PodLogOptions{Container: pod.Spec.Containers[0].Name}).
		Do(ctx).
		Raw()
	switch {
	case waitErr != nil:
		return string(logs), fmt.Errorf(
			"the pod %s/%s did not end within %s: %w\n%s",
			pod.Namespace,
			pod.Name,
			timeout,
			waitErr,
			logs,
		)
	case logErr != nil:
		return "", fmt.Errorf("reading the logs of the pod %s/%s: %w", pod.Namespace, pod.Name, logErr)
	case phase == corev1.PodFailed:
		return string(logs), fmt.Errorf("the pod %s/%s failed:\n%s", pod.Namespace, pod.Name, logs)
	}

	return string(logs), nil
}

// parseKeys reads a listing of object keys, a JSON list of strings. null is
// an empty list. Any other output is an error that quotes it, so that a
// refused listing never reads as an empty bucket.
func parseKeys(listing string) ([]string, error) {
	var keys []string
	if err := json.Unmarshal([]byte(listing), &keys); err != nil {
		return nil, fmt.Errorf("reading the key listing %q: %w", listing, err)
	}

	return keys, nil
}

func TestParseKeys(t *testing.T) {
	keys, err := parseKeys(`["a/1","a/2"]`)
	require.NoError(t, err)
	assert.Equal(t, []string{"a/1", "a/2"}, keys)

	keys, err = parseKeys("null\n")
	require.NoError(t, err)
	assert.Empty(t, keys)

	_, err = parseKeys("An error occurred (AccessDenied)")
	assert.Error(t, err)
}
