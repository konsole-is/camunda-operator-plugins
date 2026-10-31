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

// Package awskms is the AWS KMS plugin. It serves the encryption-key section
// through pluginkit: it gives each Camunda cluster one customer-managed KMS
// key with automatic rotation and an alias, and with the multi-Region
// registration a replica of the key in each region that the registration
// names.
package awskms
