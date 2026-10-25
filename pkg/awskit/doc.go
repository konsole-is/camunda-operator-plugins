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

// Package awskit is the AWS layer of the official AWS plugins. It holds the
// AWS clients behind small interfaces, and the names, ARNs, errors, policy
// documents, roles, and Pod Identity associations that more than one plugin
// uses. Package awsfake holds in-memory clients for tests. A new AWS plugin
// adds its client interface to Clients and its fake to awsfake.
package awskit
