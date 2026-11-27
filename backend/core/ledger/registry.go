// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package ledger

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
)

// Kind names an effect vocabulary. An area registers the kinds its engine posts.
type Kind interface {
	Name() string
}

// Served is true: the engine and its activation guarantees ship together. The area that owns an
// engine registers its kinds in an init; an area that registers nothing executes nothing.
const Served = true

var (
	registryMu sync.RWMutex
	registry   = map[string]struct{}{}
)

// Register records k as executable by this build. It refuses a nil or typed-nil kind, an empty
// or padded name, and a name already registered: a second registration is a wiring defect, never
// an idempotent no-op that could hide two engines claiming one vocabulary.
func Register(k Kind) error {
	if k == nil {
		return errors.New("bulk-change kind registration refused: nil kind")
	}
	if v := reflect.ValueOf(k); v.Kind() == reflect.Pointer && v.IsNil() {
		return errors.New("bulk-change kind registration refused: nil kind")
	}
	name := k.Name()
	if name == "" || strings.TrimSpace(name) != name {
		return fmt.Errorf("bulk-change kind registration refused: invalid name %q", name)
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, dup := registry[name]; dup {
		return fmt.Errorf("bulk-change kind registration refused: %q is already registered", name)
	}
	registry[name] = struct{}{}
	return nil
}

// ExecutableKinds returns a sorted copy of the registered names; callers may change it freely.
func ExecutableKinds() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]string, 0, len(registry))
	for name := range registry {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Executable reports whether this build has registered name.
func Executable(name string) bool {
	registryMu.RLock()
	defer registryMu.RUnlock()
	_, ok := registry[name]
	return ok
}
