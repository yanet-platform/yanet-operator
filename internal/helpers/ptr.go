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

package helpers

// PtrBool returns a pointer to the given bool value.
func PtrBool(b bool) *bool {
	return &b
}

// PtrTrue returns a pointer to the boolean value true.
func PtrTrue() *bool {
	t := true
	return &t
}

// PtrFalse returns a pointer to the boolean value false.
func PtrFalse() *bool {
	f := false
	return &f
}

// BoolValue dereferences a *bool, returning def when the pointer is nil.
// Use this when reading optional Enabled / HostIPC / HostNetwork flags
// from the v2alpha1 API.
func BoolValue(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

// Int32Ptr returns a pointer to the given int32 value.
func Int32Ptr(i int32) *int32 {
	return &i
}

// Int32Value dereferences a *int32, returning def when the pointer is nil.
func Int32Value(p *int32, def int32) int32 {
	if p == nil {
		return def
	}
	return *p
}
