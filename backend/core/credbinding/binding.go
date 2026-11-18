// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package credbinding

import (
	"fmt"
	"slices"
	"strings"
)

const Code = "SECRET_RESUPPLY_REQUIRED"

type Binding struct {
	kind   string
	fields map[string]string
}

func New(kind string) Binding { return Binding{kind, map[string]string{}} }
func (b Binding) With(field, value string) Binding {
	n := New(b.kind)
	for k, v := range b.fields {
		n.fields[k] = v
	}
	n.fields[field] = value
	return n
}
func Opaque(kind string, raw []byte) Binding { return New(kind).With("config", string(raw)) }
func (b Binding) Diff(after Binding) []string {
	var changed []string
	keys := map[string]bool{}
	for k := range b.fields {
		keys[k] = true
	}
	for k := range after.fields {
		keys[k] = true
	}
	for k := range keys {
		v, ok := b.fields[k]
		w, present := after.fields[k]
		if ok != present || v != w {
			changed = append(changed, k)
		}
	}
	slices.Sort(changed)
	if b.kind != after.kind {
		changed = append([]string{"type"}, changed...)
	}
	return changed
}

type ResupplyRequiredError struct {
	Subject, Token string
	Changed        []string
}

func (e *ResupplyRequiredError) Error() string {
	return fmt.Sprintf("%s %q: this update changes where its stored credential is sent (%s), so supply the secret again in the same request (secret), or clear it with null", e.Subject, e.Token, strings.Join(e.Changed, ", "))
}
func (e *ResupplyRequiredError) Extensions() map[string]any {
	return map[string]any{"code": Code, "changed": slices.Clone(e.Changed)}
}
func Guard(subject, token string, before, after Binding, secretNamed bool, exists func() (bool, error)) error {
	changed := before.Diff(after)
	if secretNamed || len(changed) == 0 {
		return nil
	}
	has, err := exists()
	if err != nil {
		return fmt.Errorf("checking stored credential: %w", err)
	}
	if !has {
		return nil
	}
	return &ResupplyRequiredError{subject, token, changed}
}
