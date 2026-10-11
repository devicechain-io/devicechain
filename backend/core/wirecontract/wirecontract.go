// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package wirecontract compares a Go wire struct with the JSON Schema that publishes it.
//
// The device-facing payloads (the inbound event envelope and its payloads, the command
// delivery and response envelopes) are decoded by plain encoding/json into Go structs, and
// documented to device authors as committed JSON Schema files. Two declarations of one
// contract drift apart unless something holds them together, and a schema that has drifted
// is worse than none: it is the document a firmware author trusts. Compare is that hold.
//
// The comparison is deliberately exact, member by member:
//
//   - the struct's json tag names and the schema's `properties` are the same set;
//   - a member is required in the schema exactly when its tag has no `omitempty`;
//   - where both sides name a JSON type, the types agree.
//
// It is strict about the struct side too. A field with no json tag is an ERROR, not a field
// matched by its Go name: encoding/json would match it case-insensitively, so its wire name
// is a coincidence of spelling that a rename changes without anyone deciding to change the
// contract.
package wirecontract

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
)

// Member is one JSON object member as one side of the contract declares it.
type Member struct {
	Name     string
	Required bool
	// Type is the JSON Schema type ("string", "integer", "number", "boolean", "object",
	// "array"), or "" when the member admits any JSON value.
	Type string
}

var (
	rawMessageType = reflect.TypeOf(json.RawMessage(nil))
	timeType       = reflect.TypeOf(time.Time{})
)

// StructMembers lists the members a struct type puts on the wire, from its json tags.
// t may be a pointer to a struct.
func StructMembers(t reflect.Type) (map[string]Member, error) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("%s is not a struct", t)
	}
	members := map[string]Member{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Anonymous {
			// encoding/json promotes an embedded struct's fields into the parent object.
			// That is not modelled here, and skipping it would miscount the members.
			return nil, fmt.Errorf("%s embeds %s; embedded structs are not supported", t, f.Type)
		}
		if !f.IsExported() {
			continue
		}
		tag, tagged := f.Tag.Lookup("json")
		if tag == "-" {
			continue
		}
		if !tagged {
			return nil, fmt.Errorf("%s.%s has no json tag: its wire name would be its Go name, "+
				"matched case-insensitively, which no one chose as the contract", t, f.Name)
		}
		parts := strings.Split(tag, ",")
		name := parts[0]
		if name == "" {
			return nil, fmt.Errorf("%s.%s has a json tag with no name", t, f.Name)
		}
		omitempty := false
		for _, opt := range parts[1:] {
			if opt == "omitempty" || opt == "omitzero" {
				omitempty = true
			}
		}
		if _, dup := members[name]; dup {
			return nil, fmt.Errorf("%s declares the json name %q twice", t, name)
		}
		typ, err := jsonType(f.Type)
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", t, f.Name, err)
		}
		members[name] = Member{Name: name, Required: !omitempty, Type: typ}
	}
	return members, nil
}

// jsonType maps a Go field type onto the JSON Schema type encoding/json reads it from.
// A kind it does not know is an error rather than a guess.
func jsonType(t reflect.Type) (string, error) {
	if t == rawMessageType {
		return "", nil
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
		if t == rawMessageType {
			return "", nil
		}
	}
	if t == timeType {
		return "string", nil
	}
	switch t.Kind() {
	case reflect.String:
		return "string", nil
	case reflect.Bool:
		return "boolean", nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "integer", nil
	case reflect.Float32, reflect.Float64:
		return "number", nil
	case reflect.Map, reflect.Struct:
		return "object", nil
	case reflect.Slice, reflect.Array:
		return "array", nil
	case reflect.Interface:
		return "", nil
	default:
		return "", fmt.Errorf("no JSON type is defined here for Go kind %s", t.Kind())
	}
}

// SchemaMembers lists the members an object schema declares. pointer is a JSON Pointer
// (RFC 6901) into doc naming the object schema, "" for the document root.
func SchemaMembers(doc []byte, pointer string) (map[string]Member, error) {
	var root any
	if err := json.Unmarshal(doc, &root); err != nil {
		return nil, fmt.Errorf("schema is not JSON: %w", err)
	}
	node, err := resolve(root, pointer)
	if err != nil {
		return nil, err
	}
	obj, ok := node.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%q is not a schema object", pointer)
	}
	if obj["type"] != "object" {
		return nil, fmt.Errorf("%q is not an object schema (type %v)", pointer, obj["type"])
	}
	props, ok := obj["properties"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%q declares no properties", pointer)
	}
	members := map[string]Member{}
	for name, p := range props {
		prop, ok := p.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%q property %q is not a schema object", pointer, name)
		}
		m := Member{Name: name}
		switch typ := prop["type"].(type) {
		case nil:
		case string:
			m.Type = typ
		default:
			return nil, fmt.Errorf("%q property %q: only a single type name is supported, got %v",
				pointer, name, prop["type"])
		}
		members[name] = m
	}
	if req, present := obj["required"]; present {
		list, ok := req.([]any)
		if !ok {
			return nil, fmt.Errorf("%q required is not an array", pointer)
		}
		for _, r := range list {
			name, ok := r.(string)
			if !ok {
				return nil, fmt.Errorf("%q required lists a non-string %v", pointer, r)
			}
			m, ok := members[name]
			if !ok {
				return nil, fmt.Errorf("%q requires %q, which it does not declare", pointer, name)
			}
			if m.Required {
				return nil, fmt.Errorf("%q requires %q twice", pointer, name)
			}
			m.Required = true
			members[name] = m
		}
	}
	return members, nil
}

func resolve(root any, pointer string) (any, error) {
	if pointer == "" {
		return root, nil
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, fmt.Errorf("JSON pointer %q must start with /", pointer)
	}
	node := root
	for _, raw := range strings.Split(pointer[1:], "/") {
		token := strings.ReplaceAll(strings.ReplaceAll(raw, "~1", "/"), "~0", "~")
		obj, ok := node.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("JSON pointer %q: %q is not inside an object", pointer, token)
		}
		if node, ok = obj[token]; !ok {
			return nil, fmt.Errorf("JSON pointer %q: no member %q", pointer, token)
		}
	}
	return node, nil
}

// Diff reports every way the two declarations disagree, one line each, sorted. Empty means
// they declare the same contract.
func Diff(schema, strct map[string]Member) []string {
	var out []string
	for name, s := range strct {
		sc, ok := schema[name]
		if !ok {
			out = append(out, fmt.Sprintf("%q is on the struct but not in the schema", name))
			continue
		}
		if s.Required != sc.Required {
			out = append(out, fmt.Sprintf("%q is required=%t on the struct (omitempty decides) but required=%t in the schema",
				name, s.Required, sc.Required))
		}
		if s.Type != sc.Type {
			out = append(out, fmt.Sprintf("%q is JSON type %q on the struct but %q in the schema",
				name, orAny(s.Type), orAny(sc.Type)))
		}
	}
	for name := range schema {
		if _, ok := strct[name]; !ok {
			out = append(out, fmt.Sprintf("%q is in the schema but not on the struct", name))
		}
	}
	sort.Strings(out)
	return out
}

func orAny(t string) string {
	if t == "" {
		return "any"
	}
	return t
}

// Compare is Diff over a schema document and a value of the struct type it publishes,
// applied recursively: a member whose Go type is a struct, or a slice, array or map of
// anything, is followed into the schema that describes it (`items` for a slice or array,
// `additionalProperties` for a map, a local `$ref` wherever one appears), so a nested struct
// is held to its schema too rather than passing because its parent's member is "an array".
func Compare(doc []byte, pointer string, v any) ([]string, error) {
	var root any
	if err := json.Unmarshal(doc, &root); err != nil {
		return nil, fmt.Errorf("schema is not JSON: %w", err)
	}
	node, err := resolve(root, pointer)
	if err != nil {
		return nil, err
	}
	c := &comparer{root: root}
	c.object(node, reflect.TypeOf(v), "")
	if c.err != nil {
		return nil, c.err
	}
	sort.Strings(c.out)
	return c.out, nil
}

type comparer struct {
	root any
	out  []string
	err  error
}

func (c *comparer) drift(path, format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	if path != "" {
		line = path + ": " + line
	}
	c.out = append(c.out, line)
}

// follow resolves a schema node through any local $refs. A non-local $ref is an error:
// this package does not fetch documents, and silently skipping one would pass vacuously.
func (c *comparer) follow(node any) (map[string]any, error) {
	for i := 0; i < 32; i++ {
		obj, ok := node.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("schema node %v is not an object", node)
		}
		ref, ok := obj["$ref"].(string)
		if !ok {
			return obj, nil
		}
		if !strings.HasPrefix(ref, "#") {
			return nil, fmt.Errorf("non-local $ref %q is not followed here", ref)
		}
		if node, ok = mustResolve(c.root, ref[1:]); !ok {
			return nil, fmt.Errorf("$ref %q resolves to nothing", ref)
		}
	}
	return nil, fmt.Errorf("$ref chain too deep")
}

func mustResolve(root any, pointer string) (any, bool) {
	n, err := resolve(root, pointer)
	return n, err == nil
}

func (c *comparer) object(node any, t reflect.Type, path string) {
	if c.err != nil {
		return
	}
	obj, err := c.follow(node)
	if err != nil {
		c.err = fmt.Errorf("%s: %w", orRoot(path), err)
		return
	}
	raw, err := json.Marshal(obj)
	if err != nil {
		c.err = err
		return
	}
	schema, err := SchemaMembers(raw, "")
	if err != nil {
		c.err = fmt.Errorf("%s: %w", orRoot(path), err)
		return
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	strct, err := StructMembers(t)
	if err != nil {
		c.err = err
		return
	}
	for _, line := range Diff(schema, strct) {
		c.drift(path, "%s", line)
	}
	props, _ := obj["properties"].(map[string]any)
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name := strings.Split(f.Tag.Get("json"), ",")[0]
		if !f.IsExported() || name == "" || name == "-" {
			continue
		}
		prop, ok := props[name]
		if !ok {
			continue // already reported by Diff
		}
		c.value(prop, f.Type, join(path, name))
	}
}

// value holds the schema of one member to the Go type that decodes it, below the level
// Diff compares: into structs, and into the elements of slices, arrays and maps.
func (c *comparer) value(node any, t reflect.Type, path string) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == rawMessageType || t == timeType || t.Kind() == reflect.Interface {
		return
	}
	switch t.Kind() {
	case reflect.Struct:
		c.object(node, t, path)
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return
		}
		c.element(node, "items", t.Elem(), path+"[]")
	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			c.err = fmt.Errorf("%s: map keys of kind %s are not supported", path, t.Key().Kind())
			return
		}
		c.element(node, "additionalProperties", t.Elem(), path+"{}")
	}
}

// element compares the schema under keyword (items / additionalProperties) with the Go
// element type, then descends into it.
func (c *comparer) element(node any, keyword string, elem reflect.Type, path string) {
	obj, err := c.follow(node)
	if err != nil {
		c.err = fmt.Errorf("%s: %w", path, err)
		return
	}
	want, err := jsonType(elem)
	if err != nil {
		c.err = fmt.Errorf("%s: %w", path, err)
		return
	}
	sub, present := obj[keyword]
	if !present {
		if want != "" {
			c.drift(path, "the struct's elements are JSON type %q but the schema declares no %s", want, keyword)
		}
		return
	}
	subObj, err := c.follow(sub)
	if err != nil {
		c.err = fmt.Errorf("%s: %w", path, err)
		return
	}
	got, _ := subObj["type"].(string)
	if got != want {
		c.drift(path, "elements are JSON type %q on the struct but %q in the schema", orAny(want), orAny(got))
		return
	}
	c.value(subObj, elem, path)
}

func join(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

func orRoot(path string) string {
	if path == "" {
		return "(root)"
	}
	return path
}
