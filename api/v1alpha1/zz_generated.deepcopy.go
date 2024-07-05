//go:build !ignore_autogenerated
// +build !ignore_autogenerated

/*
Copyright 2023 YANDEX LLC.

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

// Code generated by controller-gen. DO NOT EDIT.

package v1alpha1

import (
	"k8s.io/api/core/v1"
	runtime "k8s.io/apimachinery/pkg/runtime"
)

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *AdditionalOpts) DeepCopyInto(out *AdditionalOpts) {
	*out = *in
	if in.InitContainers != nil {
		in, out := &in.InitContainers, &out.InitContainers
		*out = make([]v1.Container, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
	if in.PostStart != nil {
		in, out := &in.PostStart, &out.PostStart
		*out = make([]NamedLifecycleHandler, len(*in))
		copy(*out, *in)
	}
	if in.Annotations != nil {
		in, out := &in.Annotations, &out.Annotations
		*out = make([]NamedAnnotations, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new AdditionalOpts.
func (in *AdditionalOpts) DeepCopy() *AdditionalOpts {
	if in == nil {
		return nil
	}
	out := new(AdditionalOpts)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *AutoDiscovery) DeepCopyInto(out *AutoDiscovery) {
	*out = *in
	out.Images = in.Images
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new AutoDiscovery.
func (in *AutoDiscovery) DeepCopy() *AutoDiscovery {
	if in == nil {
		return nil
	}
	out := new(AutoDiscovery)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *Dep) DeepCopyInto(out *Dep) {
	*out = *in
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new Dep.
func (in *Dep) DeepCopy() *Dep {
	if in == nil {
		return nil
	}
	out := new(Dep)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *DepOpts) DeepCopyInto(out *DepOpts) {
	*out = *in
	in.Controlplane.DeepCopyInto(&out.Controlplane)
	in.Dataplain.DeepCopyInto(&out.Dataplain)
	in.Bird.DeepCopyInto(&out.Bird)
	in.Announcer.DeepCopyInto(&out.Announcer)
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new DepOpts.
func (in *DepOpts) DeepCopy() *DepOpts {
	if in == nil {
		return nil
	}
	out := new(DepOpts)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *EnabledOpts) DeepCopyInto(out *EnabledOpts) {
	*out = *in
	in.Release.DeepCopyInto(&out.Release)
	in.Balancer.DeepCopyInto(&out.Balancer)
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new EnabledOpts.
func (in *EnabledOpts) DeepCopy() *EnabledOpts {
	if in == nil {
		return nil
	}
	out := new(EnabledOpts)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *Images) DeepCopyInto(out *Images) {
	*out = *in
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new Images.
func (in *Images) DeepCopy() *Images {
	if in == nil {
		return nil
	}
	out := new(Images)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *LifecycleHandler) DeepCopyInto(out *LifecycleHandler) {
	*out = *in
	if in.Exec != nil {
		in, out := &in.Exec, &out.Exec
		*out = make([]string, len(*in))
		copy(*out, *in)
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new LifecycleHandler.
func (in *LifecycleHandler) DeepCopy() *LifecycleHandler {
	if in == nil {
		return nil
	}
	out := new(LifecycleHandler)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *NamedAnnotations) DeepCopyInto(out *NamedAnnotations) {
	*out = *in
	if in.Annotations != nil {
		in, out := &in.Annotations, &out.Annotations
		*out = make(map[string]string, len(*in))
		for key, val := range *in {
			(*out)[key] = val
		}
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new NamedAnnotations.
func (in *NamedAnnotations) DeepCopy() *NamedAnnotations {
	if in == nil {
		return nil
	}
	out := new(NamedAnnotations)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *NamedLifecycleHandler) DeepCopyInto(out *NamedLifecycleHandler) {
	*out = *in
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new NamedLifecycleHandler.
func (in *NamedLifecycleHandler) DeepCopy() *NamedLifecycleHandler {
	if in == nil {
		return nil
	}
	out := new(NamedLifecycleHandler)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *OptsNames) DeepCopyInto(out *OptsNames) {
	*out = *in
	if in.InitContainers != nil {
		in, out := &in.InitContainers, &out.InitContainers
		*out = make([]string, len(*in))
		copy(*out, *in)
	}
	in.PostStart.DeepCopyInto(&out.PostStart)
	if in.Annotations != nil {
		in, out := &in.Annotations, &out.Annotations
		*out = make([]string, len(*in))
		copy(*out, *in)
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new OptsNames.
func (in *OptsNames) DeepCopy() *OptsNames {
	if in == nil {
		return nil
	}
	out := new(OptsNames)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *Yanet) DeepCopyInto(out *Yanet) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	out.Spec = in.Spec
	in.Status.DeepCopyInto(&out.Status)
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new Yanet.
func (in *Yanet) DeepCopy() *Yanet {
	if in == nil {
		return nil
	}
	out := new(Yanet)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *Yanet) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *YanetConfig) DeepCopyInto(out *YanetConfig) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	out.Status = in.Status
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new YanetConfig.
func (in *YanetConfig) DeepCopy() *YanetConfig {
	if in == nil {
		return nil
	}
	out := new(YanetConfig)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *YanetConfig) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *YanetConfigList) DeepCopyInto(out *YanetConfigList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]YanetConfig, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new YanetConfigList.
func (in *YanetConfigList) DeepCopy() *YanetConfigList {
	if in == nil {
		return nil
	}
	out := new(YanetConfigList)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *YanetConfigList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *YanetConfigSpec) DeepCopyInto(out *YanetConfigSpec) {
	*out = *in
	out.AutoDiscovery = in.AutoDiscovery
	in.EnabledOpts.DeepCopyInto(&out.EnabledOpts)
	in.AdditionalOpts.DeepCopyInto(&out.AdditionalOpts)
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new YanetConfigSpec.
func (in *YanetConfigSpec) DeepCopy() *YanetConfigSpec {
	if in == nil {
		return nil
	}
	out := new(YanetConfigSpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *YanetConfigStatus) DeepCopyInto(out *YanetConfigStatus) {
	*out = *in
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new YanetConfigStatus.
func (in *YanetConfigStatus) DeepCopy() *YanetConfigStatus {
	if in == nil {
		return nil
	}
	out := new(YanetConfigStatus)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *YanetList) DeepCopyInto(out *YanetList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]Yanet, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new YanetList.
func (in *YanetList) DeepCopy() *YanetList {
	if in == nil {
		return nil
	}
	out := new(YanetList)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *YanetList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *YanetSpec) DeepCopyInto(out *YanetSpec) {
	*out = *in
	out.Announcer = in.Announcer
	out.Controlplane = in.Controlplane
	out.Dataplane = in.Dataplane
	out.Bird = in.Bird
	out.PrepareJob = in.PrepareJob
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new YanetSpec.
func (in *YanetSpec) DeepCopy() *YanetSpec {
	if in == nil {
		return nil
	}
	out := new(YanetSpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *YanetStatus) DeepCopyInto(out *YanetStatus) {
	*out = *in
	if in.Pods != nil {
		in, out := &in.Pods, &out.Pods
		*out = make(map[v1.PodPhase][]string, len(*in))
		for key, val := range *in {
			var outVal []string
			if val == nil {
				(*out)[key] = nil
			} else {
				in, out := &val, &outVal
				*out = make([]string, len(*in))
				copy(*out, *in)
			}
			(*out)[key] = outVal
		}
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new YanetStatus.
func (in *YanetStatus) DeepCopy() *YanetStatus {
	if in == nil {
		return nil
	}
	out := new(YanetStatus)
	in.DeepCopyInto(out)
	return out
}
