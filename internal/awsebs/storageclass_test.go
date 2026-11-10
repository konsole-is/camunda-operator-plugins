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

package awsebs

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/utils/ptr"
)

const testKey = "arn:aws:kms:eu-west-1:111122223333:key/1234abcd-12ab-34cd-56ef-1234567890ab"

func TestStorageClassEncryptsWithTheKeyAndTheDefaults(t *testing.T) {
	sc := StorageClass("camunda-team-a-orders-0123456789abcdef", Params{}, testKey)

	assert.Equal(t, "camunda-team-a-orders-0123456789abcdef", sc.Name)
	assert.Equal(t, "ebs.csi.aws.com", sc.Provisioner)
	assert.Equal(t, map[string]string{
		"type":                      "gp3",
		"csi.storage.k8s.io/fstype": "ext4",
		"encrypted":                 "true",
		"kmsKeyId":                  testKey,
	}, sc.Parameters)
	assert.Equal(t, ptr.To(true), sc.AllowVolumeExpansion)
	assert.Equal(t, ptr.To(storagev1.VolumeBindingWaitForFirstConsumer), sc.VolumeBindingMode)
	assert.Equal(t, ptr.To(corev1.PersistentVolumeReclaimDelete), sc.ReclaimPolicy)
}

func TestStorageClassTakesEachParameter(t *testing.T) {
	sc := StorageClass("sc", Params{
		Type: "io2", IOPS: ptr.To[int64](6000), FSType: "xfs", ReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
	}, testKey)

	assert.Equal(t, "io2", sc.Parameters["type"])
	assert.Equal(t, "6000", sc.Parameters["iops"])
	assert.Equal(t, "xfs", sc.Parameters["csi.storage.k8s.io/fstype"])
	assert.NotContains(t, sc.Parameters, "throughput")
	assert.Equal(t, ptr.To(corev1.PersistentVolumeReclaimRetain), sc.ReclaimPolicy)
}

func TestStorageClassSetsTheThroughputOfGP3(t *testing.T) {
	sc := StorageClass("sc", Params{Throughput: ptr.To[int64](250)}, testKey)

	assert.Equal(t, "250", sc.Parameters["throughput"])
}

func TestChangedImmutable(t *testing.T) {
	base := StorageClass("sc", Params{}, testKey)
	otherProvisioner := StorageClass("sc", Params{}, testKey)
	otherProvisioner.Provisioner = "example.com/other"
	immediate := StorageClass("sc", Params{}, testKey)
	immediate.VolumeBindingMode = ptr.To(storagev1.VolumeBindingImmediate)
	tests := []struct {
		name    string
		desired *storagev1.StorageClass
		want    []string
	}{
		{"nothing changed", StorageClass("sc", Params{}, testKey), nil},
		{
			"the volume type",
			StorageClass("sc", Params{Type: "io1", IOPS: ptr.To[int64](3000)}, testKey),
			[]string{"parameters.iops", "parameters.type"},
		},
		{"the key", StorageClass("sc", Params{}, testKey+"0"), []string{"parameters.kmsKeyId"}},
		{
			"the reclaim policy",
			StorageClass("sc", Params{ReclaimPolicy: corev1.PersistentVolumeReclaimRetain}, testKey),
			[]string{"reclaimPolicy"},
		},
		{"the provisioner", otherProvisioner, []string{"provisioner"}},
		{"the volume binding mode", immediate, []string{"volumeBindingMode"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ChangedImmutable(base, tt.desired))
		})
	}
}

func TestChangedImmutableIgnoresTheMutableFields(t *testing.T) {
	live := StorageClass("sc", Params{}, testKey)
	live.AllowVolumeExpansion = ptr.To(false)
	live.Labels = map[string]string{"team": "a"}
	live.MountOptions = []string{"noatime"}

	assert.Empty(t, ChangedImmutable(live, StorageClass("sc", Params{}, testKey)))
}
