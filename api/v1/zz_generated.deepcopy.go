//go:build !ignore_autogenerated

// Code generated by controller-gen. DO NOT EDIT.

package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtime "k8s.io/apimachinery/pkg/runtime"
)

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *Composition) DeepCopyInto(out *Composition) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new Composition.
func (in *Composition) DeepCopy() *Composition {
	if in == nil {
		return nil
	}
	out := new(Composition)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *Composition) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *CompositionList) DeepCopyInto(out *CompositionList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]Composition, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new CompositionList.
func (in *CompositionList) DeepCopy() *CompositionList {
	if in == nil {
		return nil
	}
	out := new(CompositionList)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *CompositionList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *CompositionSpec) DeepCopyInto(out *CompositionSpec) {
	*out = *in
	out.Synthesizer = in.Synthesizer
	if in.ReconcileInterval != nil {
		in, out := &in.ReconcileInterval, &out.ReconcileInterval
		*out = new(metav1.Duration)
		**out = **in
	}
	if in.Inputs != nil {
		in, out := &in.Inputs, &out.Inputs
		*out = make([]InputRef, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new CompositionSpec.
func (in *CompositionSpec) DeepCopy() *CompositionSpec {
	if in == nil {
		return nil
	}
	out := new(CompositionSpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *CompositionStatus) DeepCopyInto(out *CompositionStatus) {
	*out = *in
	if in.CurrentState != nil {
		in, out := &in.CurrentState, &out.CurrentState
		*out = new(Synthesis)
		(*in).DeepCopyInto(*out)
	}
	if in.PreviousState != nil {
		in, out := &in.PreviousState, &out.PreviousState
		*out = new(Synthesis)
		(*in).DeepCopyInto(*out)
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new CompositionStatus.
func (in *CompositionStatus) DeepCopy() *CompositionStatus {
	if in == nil {
		return nil
	}
	out := new(CompositionStatus)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *InputRef) DeepCopyInto(out *InputRef) {
	*out = *in
	if in.Resource != nil {
		in, out := &in.Resource, &out.Resource
		*out = new(ResourceInputRef)
		**out = **in
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new InputRef.
func (in *InputRef) DeepCopy() *InputRef {
	if in == nil {
		return nil
	}
	out := new(InputRef)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *Manifest) DeepCopyInto(out *Manifest) {
	*out = *in
	if in.ReconcileInterval != nil {
		in, out := &in.ReconcileInterval, &out.ReconcileInterval
		*out = new(metav1.Duration)
		**out = **in
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new Manifest.
func (in *Manifest) DeepCopy() *Manifest {
	if in == nil {
		return nil
	}
	out := new(Manifest)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *ResourceInputRef) DeepCopyInto(out *ResourceInputRef) {
	*out = *in
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new ResourceInputRef.
func (in *ResourceInputRef) DeepCopy() *ResourceInputRef {
	if in == nil {
		return nil
	}
	out := new(ResourceInputRef)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *ResourceSlice) DeepCopyInto(out *ResourceSlice) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new ResourceSlice.
func (in *ResourceSlice) DeepCopy() *ResourceSlice {
	if in == nil {
		return nil
	}
	out := new(ResourceSlice)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *ResourceSlice) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *ResourceSliceList) DeepCopyInto(out *ResourceSliceList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]ResourceSlice, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new ResourceSliceList.
func (in *ResourceSliceList) DeepCopy() *ResourceSliceList {
	if in == nil {
		return nil
	}
	out := new(ResourceSliceList)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *ResourceSliceList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *ResourceSliceRef) DeepCopyInto(out *ResourceSliceRef) {
	*out = *in
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new ResourceSliceRef.
func (in *ResourceSliceRef) DeepCopy() *ResourceSliceRef {
	if in == nil {
		return nil
	}
	out := new(ResourceSliceRef)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *ResourceSliceSpec) DeepCopyInto(out *ResourceSliceSpec) {
	*out = *in
	if in.Resources != nil {
		in, out := &in.Resources, &out.Resources
		*out = make([]Manifest, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new ResourceSliceSpec.
func (in *ResourceSliceSpec) DeepCopy() *ResourceSliceSpec {
	if in == nil {
		return nil
	}
	out := new(ResourceSliceSpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *ResourceSliceStatus) DeepCopyInto(out *ResourceSliceStatus) {
	*out = *in
	if in.Resources != nil {
		in, out := &in.Resources, &out.Resources
		*out = make([]ResourceState, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new ResourceSliceStatus.
func (in *ResourceSliceStatus) DeepCopy() *ResourceSliceStatus {
	if in == nil {
		return nil
	}
	out := new(ResourceSliceStatus)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *ResourceState) DeepCopyInto(out *ResourceState) {
	*out = *in
	if in.Ready != nil {
		in, out := &in.Ready, &out.Ready
		*out = new(bool)
		**out = **in
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new ResourceState.
func (in *ResourceState) DeepCopy() *ResourceState {
	if in == nil {
		return nil
	}
	out := new(ResourceState)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *Synthesis) DeepCopyInto(out *Synthesis) {
	*out = *in
	if in.PodCreation != nil {
		in, out := &in.PodCreation, &out.PodCreation
		*out = (*in).DeepCopy()
	}
	if in.ResourceSlices != nil {
		in, out := &in.ResourceSlices, &out.ResourceSlices
		*out = make([]*ResourceSliceRef, len(*in))
		for i := range *in {
			if (*in)[i] != nil {
				in, out := &(*in)[i], &(*out)[i]
				*out = new(ResourceSliceRef)
				**out = **in
			}
		}
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new Synthesis.
func (in *Synthesis) DeepCopy() *Synthesis {
	if in == nil {
		return nil
	}
	out := new(Synthesis)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *Synthesizer) DeepCopyInto(out *Synthesizer) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new Synthesizer.
func (in *Synthesizer) DeepCopy() *Synthesizer {
	if in == nil {
		return nil
	}
	out := new(Synthesizer)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *Synthesizer) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *SynthesizerList) DeepCopyInto(out *SynthesizerList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]Synthesizer, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new SynthesizerList.
func (in *SynthesizerList) DeepCopy() *SynthesizerList {
	if in == nil {
		return nil
	}
	out := new(SynthesizerList)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *SynthesizerList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *SynthesizerRef) DeepCopyInto(out *SynthesizerRef) {
	*out = *in
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new SynthesizerRef.
func (in *SynthesizerRef) DeepCopy() *SynthesizerRef {
	if in == nil {
		return nil
	}
	out := new(SynthesizerRef)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *SynthesizerSpec) DeepCopyInto(out *SynthesizerSpec) {
	*out = *in
	if in.Command != nil {
		in, out := &in.Command, &out.Command
		*out = make([]string, len(*in))
		copy(*out, *in)
	}
	out.Timeout = in.Timeout
	out.PodTimeout = in.PodTimeout
	out.RolloutCooldown = in.RolloutCooldown
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new SynthesizerSpec.
func (in *SynthesizerSpec) DeepCopy() *SynthesizerSpec {
	if in == nil {
		return nil
	}
	out := new(SynthesizerSpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *SynthesizerStatus) DeepCopyInto(out *SynthesizerStatus) {
	*out = *in
	if in.LastRolloutTime != nil {
		in, out := &in.LastRolloutTime, &out.LastRolloutTime
		*out = (*in).DeepCopy()
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new SynthesizerStatus.
func (in *SynthesizerStatus) DeepCopy() *SynthesizerStatus {
	if in == nil {
		return nil
	}
	out := new(SynthesizerStatus)
	in.DeepCopyInto(out)
	return out
}
