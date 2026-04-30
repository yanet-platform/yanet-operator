/*
Copyright 2023-2026 YANDEX LLC.

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

// Package events wraps the controller-runtime EventRecorder accessor.
// We use the new k8s.io/client-go/tools/events API (EventRecorder.Eventf
// with regarding/related/action/note) introduced in controller-runtime
// v0.23 — the older record.EventRecorder accessor is deprecated.
package events

import (
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
)

// NewRecorderFor returns an events.EventRecorder for the named source.
func NewRecorderFor(mgr ctrl.Manager, name string) events.EventRecorder {
	return mgr.GetEventRecorder(name)
}
