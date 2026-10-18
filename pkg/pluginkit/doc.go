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

// Package pluginkit is the shared library of every plugin. A plugin
// implements [Plugin] and calls [Setup]. pluginkit publishes the registrations
// of the plugin, holds the finalizer of each request, validates and renders
// its parameters, calls the plugin, applies the objects and the grants that it
// returns, and writes the status of the request once per reconcile.
package pluginkit
