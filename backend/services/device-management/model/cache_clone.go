// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"encoding/json"

	"gorm.io/datatypes"
)

// The clone functions below are what lets the cached API read the in-process tier's DECODED
// values (messaging.GetCloned) instead of decoding JSON on every hit.
//
// 🔴 EACH ONE MUST SHARE NO MEMORY WITH ITS ARGUMENT. The argument is the value the cache
// keeps for every reader, so a slice, map or pointer left shared lets one event's write
// show up in the next event's read. Each starts from a struct copy, which carries every
// plain field (including one added later), and then replaces every pointer, slice and map
// field with a copy. A field added with one of those kinds is NOT covered by the struct
// copy, and TestCacheClonesShareNoMemory fails for it: it fills every field of the type by
// reflection and walks both values for a shared address.
//
// A nil stays nil and an empty value stays empty, because JSON decoding told the two apart
// and callers may too.

func cloneBool(in *bool) *bool {
	v := *in
	return &v
}

func cloneMemberships(in *[]GroupMembership) *[]GroupMembership {
	out := cloneSlice(*in)
	return &out
}

func cloneProfileResolution(in *ProfileResolution) *ProfileResolution {
	out := *in
	out.Metrics = cloneResolvedMetrics(in.Metrics)
	return &out
}

func cloneResolvedMetrics(in []ResolvedMetric) []ResolvedMetric {
	if in == nil {
		return nil
	}
	out := make([]ResolvedMetric, len(in))
	for i := range in {
		out[i] = in[i]
		out[i].Unit = clonePtr(in[i].Unit)
		out[i].MinValue = clonePtr(in[i].MinValue)
		out[i].MaxValue = clonePtr(in[i].MaxValue)
		out[i].Enum = cloneSlice(in[i].Enum)
	}
	return out
}

func cloneRelationshipResults(in *EntityRelationshipSearchResults) *EntityRelationshipSearchResults {
	out := *in
	if in.Results != nil {
		out.Results = make([]EntityRelationship, len(in.Results))
		for i := range in.Results {
			out.Results[i] = cloneEntityRelationship(in.Results[i])
		}
	}
	return &out
}

func cloneEntityRelationship(in EntityRelationship) EntityRelationship {
	out := in
	out.Metadata = cloneJSONPtr(in.Metadata)
	out.RelationshipType = cloneEntityRelationshipType(in.RelationshipType)
	return out
}

func cloneEntityRelationshipType(in EntityRelationshipType) EntityRelationshipType {
	out := in
	out.Metadata = cloneJSONPtr(in.Metadata)
	return out
}

func cloneCachedDevice(in *Device) *Device {
	out := cloneDeviceValue(*in)
	return &out
}

func cloneDeviceValue(in Device) Device {
	out := in
	out.Metadata = cloneJSONPtr(in.Metadata)
	if in.DeviceType != nil {
		dt := cloneDeviceTypeValue(*in.DeviceType)
		out.DeviceType = &dt
	}
	return out
}

func cloneDeviceTypeValue(in DeviceType) DeviceType {
	out := in
	out.Metadata = cloneJSONPtr(in.Metadata)
	out.ProfileId = clonePtr(in.ProfileId)
	if in.Profile != nil {
		out.Profile = cloneDeviceProfile(in.Profile)
	}
	if in.Devices != nil {
		out.Devices = make([]Device, len(in.Devices))
		for i := range in.Devices {
			out.Devices[i] = cloneDeviceValue(in.Devices[i])
		}
	}
	return out
}

// cloneDeviceProfile copies a profile through its JSON form, which is exactly the form the
// cache stores it in, so the result is what a decode of the cached bytes would have
// produced. The device path never populates it (the resolver preloads a device's type and
// nothing beneath it), so the cost of the round trip is not paid per event; doing it this
// way means the profile's deep tree of definitions has no second, hand-kept copy to drift.
func cloneDeviceProfile(in *DeviceProfile) *DeviceProfile {
	raw, err := json.Marshal(in)
	if err != nil {
		panic("model: a DeviceProfile held by the cache did not encode: " + err.Error())
	}
	out := new(DeviceProfile)
	if err := json.Unmarshal(raw, out); err != nil {
		panic("model: a DeviceProfile held by the cache did not decode: " + err.Error())
	}
	return out
}

func clonePtr[T any](in *T) *T {
	if in == nil {
		return nil
	}
	v := *in
	return &v
}

func cloneSlice[T any](in []T) []T {
	if in == nil {
		return nil
	}
	out := make([]T, len(in))
	copy(out, in)
	return out
}

func cloneJSONPtr(in *datatypes.JSON) *datatypes.JSON {
	if in == nil {
		return nil
	}
	out := datatypes.JSON(cloneSlice([]byte(*in)))
	return &out
}
