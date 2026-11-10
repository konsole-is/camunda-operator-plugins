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
	"maps"
	"slices"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

// The provisioner, the input, the output, and the defaults of the plugin.
const (
	// Provisioner is the provisioner of the EBS CSI driver.
	Provisioner = "ebs.csi.aws.com"
	// InputKey is the input that holds the ARN of the KMS key.
	InputKey = "key"
	// OutputStorageClassName is the output that holds the name of the
	// StorageClass.
	OutputStorageClassName = "storageClassName"
	// DefaultType is the EBS volume type of a request that sets none.
	DefaultType = "gp3"
	// DefaultFSType is the file system of a request that sets none.
	DefaultFSType = "ext4"
)

// The StorageClass parameters of the EBS CSI driver.
const (
	paramType       = "type"
	paramIOPS       = "iops"
	paramThroughput = "throughput"
	paramFSType     = "csi.storage.k8s.io/fstype"
	paramEncrypted  = "encrypted"
	paramKMSKeyID   = "kmsKeyId"
)

// Params are the parameters of a request.
type Params struct {
	// Type is the EBS volume type. Empty is DefaultType.
	Type string `json:"type,omitempty"`
	// IOPS is the IOPS of each volume. Nil leaves it to the driver.
	IOPS *int64 `json:"iops,omitempty"`
	// Throughput is the throughput of each volume in MiB/s. Nil leaves it to
	// the driver.
	Throughput *int64 `json:"throughput,omitempty"`
	// FSType is the file system of each volume. Empty is DefaultFSType.
	FSType string `json:"fsType,omitempty"`
	// ReclaimPolicy is the reclaim policy of each volume. Empty is Delete.
	ReclaimPolicy corev1.PersistentVolumeReclaimPolicy `json:"reclaimPolicy,omitempty"`
}

// StorageClass returns the StorageClass name of the EBS CSI driver that
// encrypts each volume with the KMS key keyARN, with the parameters p. It
// allows volume expansion and binds a volume when its first pod is scheduled.
func StorageClass(name string, p Params, keyARN string) *storagev1.StorageClass {
	parameters := map[string]string{
		paramType:      DefaultType,
		paramFSType:    DefaultFSType,
		paramEncrypted: "true",
		paramKMSKeyID:  keyARN,
	}
	if p.Type != "" {
		parameters[paramType] = p.Type
	}
	if p.FSType != "" {
		parameters[paramFSType] = p.FSType
	}
	if p.IOPS != nil {
		parameters[paramIOPS] = strconv.FormatInt(*p.IOPS, 10)
	}
	if p.Throughput != nil {
		parameters[paramThroughput] = strconv.FormatInt(*p.Throughput, 10)
	}

	reclaim := corev1.PersistentVolumeReclaimDelete
	if p.ReclaimPolicy != "" {
		reclaim = p.ReclaimPolicy
	}

	return &storagev1.StorageClass{
		TypeMeta:             metav1.TypeMeta{APIVersion: storagev1.SchemeGroupVersion.String(), Kind: "StorageClass"},
		ObjectMeta:           metav1.ObjectMeta{Name: name},
		Provisioner:          Provisioner,
		Parameters:           parameters,
		ReclaimPolicy:        &reclaim,
		AllowVolumeExpansion: ptr.To(true),
		VolumeBindingMode:    ptr.To(storagev1.VolumeBindingWaitForFirstConsumer),
	}
}

// ChangedImmutable returns the sorted names of the fields that a StorageClass
// cannot change and that differ between live and desired: provisioner, each
// key of parameters as parameters.<key>, reclaimPolicy, and
// volumeBindingMode. It returns nil when none differs. A nil pointer is
// unset.
func ChangedImmutable(live, desired *storagev1.StorageClass) []string {
	var changed []string
	if live.Provisioner != desired.Provisioner {
		changed = append(changed, "provisioner")
	}

	keys := maps.Clone(live.Parameters)
	if keys == nil {
		keys = map[string]string{}
	}
	maps.Copy(keys, desired.Parameters)
	for key := range keys {
		lv, lok := live.Parameters[key]
		dv, dok := desired.Parameters[key]
		if lok != dok || lv != dv {
			changed = append(changed, "parameters."+key)
		}
	}

	if !ptr.Equal(live.ReclaimPolicy, desired.ReclaimPolicy) {
		changed = append(changed, "reclaimPolicy")
	}

	if !ptr.Equal(live.VolumeBindingMode, desired.VolumeBindingMode) {
		changed = append(changed, "volumeBindingMode")
	}

	slices.Sort(changed)
	return changed
}
