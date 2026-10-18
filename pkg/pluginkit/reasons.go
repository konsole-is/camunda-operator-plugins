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

package pluginkit

// The reasons that pluginkit sets beside those of the api module.
const (
	// ReasonUnhealthy means that the health check of the plugin failed. It is
	// the reason of Ready=False on a registration, with the error as message.
	ReasonUnhealthy = "Unhealthy"
	// ReasonReconcileError means that the plugin failed to reconcile the
	// request. The message is the error.
	ReasonReconcileError = "ReconcileError"
	// ReasonGrantTemplateInvalid means that a grant template of the
	// registration does not render for the request.
	ReasonGrantTemplateInvalid = "GrantTemplateInvalid"
	// ReasonDeleteFailed means that the plugin failed to apply the deletion
	// policy of a deleted request. The finalizer stays.
	ReasonDeleteFailed = "DeleteFailed"
)
