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

import "github.com/konsole-is/camunda-operator-plugins/pkg/awskit"

// resourceName returns the one name of the bucket, the user, and the policy
// of the request namespace/request.
func resourceName(namespace, request string) string {
	return awskit.BucketName(namespace, request)
}

// secretName returns the name of the Secret that holds the access key of the
// request.
func secretName(request string) string {
	return request + "-rustfs"
}
