// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package credbinding

import (
	"encoding/json"
	"reflect"
	"strings"
)

// Fields binds every parsed field except explicitly neutral fields, including future fields.
// Nested structs are traversed; a pointer's presence is itself binding.
func Fields(b Binding, value any, neutral map[string]bool) Binding {
	return fields(b, reflect.ValueOf(value), "", neutral)
}
func fields(b Binding, v reflect.Value, prefix string, neutral map[string]bool) Binding {
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return b
		}
		v = v.Elem()
	}
	t := v.Type()
	for i := 0; i < v.NumField(); i++ {
		f := t.Field(i)
		x := v.Field(i)
		name := strings.Split(f.Tag.Get("json"), ",")[0]
		if f.Anonymous {
			b = fields(b, x, prefix, neutral)
			continue
		}
		if name == "" {
			name = f.Name
		}
		name = prefix + name
		if neutral[name] {
			continue
		}
		if x.Kind() == reflect.Pointer && x.Type().Elem().Kind() == reflect.Struct {
			b = b.With(name, map[bool]string{true: "present", false: "absent"}[!x.IsNil()])
			if !x.IsNil() {
				b = fields(b, x.Elem(), name+".", neutral)
			}
			continue
		}
		raw, _ := json.Marshal(x.Interface())
		b = b.With(name, string(raw))
	}
	return b
}
