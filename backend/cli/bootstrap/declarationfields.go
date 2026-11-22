// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
)

// unknownDeclarationFields lists the paths in a declaration, as the cluster holds it, that this
// dcctl's InstanceSpec does not have.
//
// 🔴 WHY A WRITE HAS TO ASK THIS FIRST. dcctl writes a declaration as a whole: it decodes the
// object into the Go struct it was built with, changes it, and Updates it (writeInstanceDecl,
// updateInstanceMetadata). A field the struct does not know does not survive that decode, so
// the Update sends an object without it and the API server stores what it was sent. A newer
// dcctl, or a newer schema served after `dcctl install` moved the cluster, can put a field there
// that this binary has never heard of, and an ordinary re-run would erase it without a word.
// Refusing is the only safe answer, because the fix is to use a dcctl that knows the field.
//
// Only the top level and the spec are walked. Metadata is a free-form map that the decode keeps
// whole, and status is a subresource that an Update of the object does not write.
func unknownDeclarationFields(obj map[string]interface{}) []string {
	var out []string
	for k := range obj {
		switch k {
		case "apiVersion", "kind", "metadata", "spec", "status":
		default:
			out = append(out, k)
		}
	}
	if spec, ok := obj["spec"].(map[string]interface{}); ok {
		out = append(out, unknownJSONFields(spec, reflect.TypeOf(dcv1beta1.InstanceSpec{}), "spec")...)
	}
	sort.Strings(out)
	return out
}

// unknownJSONFields walks a decoded JSON object against a Go struct type by JSON field name.
// Maps keep arbitrary keys (their values are walked when they are structs); slices are walked
// element by element.
func unknownJSONFields(v map[string]interface{}, t reflect.Type, path string) []string {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}
	fields := jsonFieldsOf(t)
	var out []string
	for k, val := range v {
		ft, ok := fields[k]
		if !ok {
			out = append(out, path+"."+k)
			continue
		}
		out = append(out, unknownInValue(val, ft, path+"."+k)...)
	}
	return out
}

func unknownInValue(val interface{}, t reflect.Type, path string) []string {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	switch x := val.(type) {
	case map[string]interface{}:
		switch t.Kind() {
		case reflect.Struct:
			return unknownJSONFields(x, t, path)
		case reflect.Map:
			var out []string
			for k, mv := range x {
				out = append(out, unknownInValue(mv, t.Elem(), fmt.Sprintf("%s[%s]", path, k))...)
			}
			return out
		}
	case []interface{}:
		if t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
			var out []string
			for i, e := range x {
				out = append(out, unknownInValue(e, t.Elem(), fmt.Sprintf("%s[%d]", path, i))...)
			}
			return out
		}
	}
	return nil
}

// jsonFieldsOf maps a struct's JSON field names to their types, following inlined (embedded or
// `,inline`) structs the way encoding/json does.
func jsonFieldsOf(t reflect.Type) map[string]reflect.Type {
	out := map[string]reflect.Type{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		name, opts, _ := strings.Cut(tag, ",")
		if name == "-" {
			continue
		}
		if (f.Anonymous && name == "") || strings.Contains(","+opts+",", ",inline,") {
			ft := f.Type
			for ft.Kind() == reflect.Ptr {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				for k, v := range jsonFieldsOf(ft) {
					out[k] = v
				}
				continue
			}
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		out[name] = f.Type
	}
	return out
}

// errDeclarationFromNewerDcctl is the refusal a write returns over a declaration carrying fields
// this dcctl does not know.
func errDeclarationFromNewerDcctl(instance string, fields []string) error {
	return fmt.Errorf("the declaration for instance %q carries fields this dcctl does not know (%s); "+
		"writing it would erase them, because dcctl replaces the declaration as a whole. A newer dcctl, "+
		"or the definitions a newer `dcctl install` put on this cluster, wrote them: use that dcctl. "+
		"Nothing has been changed", instance, strings.Join(fields, ", "))
}
