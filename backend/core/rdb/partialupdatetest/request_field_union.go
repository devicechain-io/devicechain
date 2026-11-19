// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package partialupdatetest

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

// everyRequestFieldIsDriven closes the gap between per-family limitations: a
// complementary fixture must actually declare the field it is said to drive.
func everyRequestFieldIsDriven[A any](t *testing.T, s Suite[A]) {
	groups := map[reflect.Type][]Family[A]{}
	var types []reflect.Type
	for _, fam := range s.Families {
		rt := reflect.TypeOf(fam.NewRequest())
		if rt == nil || rt.Kind() != reflect.Ptr || rt.Elem().Kind() != reflect.Struct {
			t.Errorf("%s: NewRequest() returned %v, want a pointer to a struct", fam.Name, rt)
			continue
		}
		if _, exists := groups[rt]; !exists {
			types = append(types, rt)
		}
		groups[rt] = append(groups[rt], fam)
	}

	// A bare Go type name is the key, so two request types of one name in different
	// packages would share an exemption whose tests drive only one of them: refuse it.
	byName := map[string]reflect.Type{}
	ambiguous := map[string]bool{}
	for _, rt := range types {
		name := rt.Elem().Name()
		if other, ok := byName[name]; ok && other != rt {
			ambiguous[name] = true
		}
		byName[name] = rt
		// A promoted field of an embedded struct is a field callers can send, but neither
		// this check nor EveryRequestFieldIsDeclared enumerates promoted fields: refuse the
		// shape rather than certify a request with fields nobody looked at.
		for i := 0; i < rt.Elem().NumField(); i++ {
			if sf := rt.Elem().Field(i); sf.Anonymous {
				t.Errorf("%s embeds %s: embedded request fields are not supported by this harness; declare the fields directly",
					rt.Elem().Name(), sf.Type)
			}
		}
	}

	// Only the field component is case-insensitive; type names are Go names.
	external := map[string]bool{}
	keys := make([]string, 0, len(s.ExternallyDriven))
	for key := range s.ExternallyDriven {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	normalized := map[string]string{}
	for _, key := range keys {
		typeName, fieldName, _ := strings.Cut(key, ".")
		fieldName = strings.ToLower(fieldName)
		valid := true
		if first, dup := normalized[typeName+"."+fieldName]; dup {
			t.Errorf("ExternallyDriven %q duplicates %q: one exemption per field", key, first)
			valid = false
		}
		normalized[typeName+"."+fieldName] = key
		if ambiguous[typeName] {
			t.Errorf("ExternallyDriven %q is ambiguous: more than one request type in this suite is named %s", key, typeName)
			valid = false
		}
		if strings.TrimSpace(s.ExternallyDriven[key]) == "" {
			t.Errorf("ExternallyDriven %q needs a non-empty reason naming the dedicated tests", key)
			valid = false
		}
		matched := false
		for _, rt := range types {
			if rt.Elem().Name() != typeName {
				continue
			}
			for i := 0; i < rt.Elem().NumField(); i++ {
				sf := rt.Elem().Field(i)
				if sf.PkgPath != "" || strings.ToLower(sf.Name) != fieldName {
					continue
				}
				matched = true
				for _, fam := range groups[rt] {
					for _, f := range fam.Fields {
						if strings.EqualFold(f.Name, fieldName) {
							t.Errorf("ExternallyDriven %q is stale: family %q declares the field", key, fam.Name)
							valid = false
						}
					}
					limited := false
					for name := range fam.FieldLimitations {
						if strings.EqualFold(name, fieldName) {
							limited = true
						}
					}
					if !limited {
						t.Errorf("ExternallyDriven %q is stale: family %q does not limit the field; every family must limit it", key, fam.Name)
						valid = false
					}
				}
			}
		}
		if !matched {
			t.Errorf("ExternallyDriven %q matches no exported request field used by a family", key)
			valid = false
		}
		if valid {
			external[typeName+"."+fieldName] = true
		}
	}

	for _, rt := range types {
		families := groups[rt]
		declared := map[string]bool{}
		limited := map[string][]string{}
		for _, fam := range families {
			for _, f := range fam.Fields {
				declared[strings.ToLower(f.Name)] = true
			}
			for name := range fam.FieldLimitations {
				key := strings.ToLower(name)
				limited[key] = append(limited[key], fam.Name)
			}
		}
		for i := 0; i < rt.Elem().NumField(); i++ {
			sf := rt.Elem().Field(i)
			key := strings.ToLower(sf.Name)
			if sf.PkgPath != "" || declared[key] || external[rt.Elem().Name()+"."+key] {
				continue
			}
			t.Errorf("%s.%s is declared by no family (limited by families %v); each claims another family drives it; none does",
				rt.Elem().Name(), key, limited[key])
		}
	}
}
