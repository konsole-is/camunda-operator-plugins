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

// The reasons that the plugin sets on a request, beside those of pluginkit.
const (
	// ReasonInvalidKey means that the input key is not the ARN of a KMS key.
	// The outputs are not ready, and the plugin writes no StorageClass.
	ReasonInvalidKey = "InvalidKey"
	// ReasonStorageClassNameTaken means that a StorageClass of the name of
	// the request exists and is not of the request. The plugin never changes
	// or deletes it, and the outputs are not ready.
	ReasonStorageClassNameTaken = "StorageClassNameTaken"
	// ReasonStorageClassDeleting means that the old StorageClass of the
	// request is still deleting, for example while a finalizer holds it. The
	// plugin creates the new one when it is gone, and the outputs are not
	// ready.
	ReasonStorageClassDeleting = "StorageClassDeleting"
)
