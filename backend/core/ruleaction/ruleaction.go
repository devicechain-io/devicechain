// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package ruleaction identifies authoring authority without importing the engine.
// Its conservative scan MUST stay a SUPERSET of what the engine executes,
// including duplicate keys and encoding/json's Unicode case folding.
package ruleaction

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/devicechain-io/dc-microservice/auth"
)

// Set is the union of action types, with Unknown marking unsafe input.
type Set struct {
	Types   map[string]bool
	Unknown bool
}

// Classified reports whether a type has an explicit authority classification.
func Classified(kind string) bool {
	switch kind {
	case "raiseAlarm", "sendCommand", "httpCall", "publish":
		return true
	}
	return false
}

// object visits every member, including duplicates. EqualFold uses the same
// Unicode simple-fold equivalence as encoding/json's foldName (long s included).
func object(raw []byte, visit func(string, json.RawMessage) error) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	t, err := d.Token()
	if err != nil || t != json.Delim('{') {
		return fmt.Errorf("rule action scan: expected object")
	}
	var visitErr error
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return err
		}
		var value json.RawMessage
		if err := d.Decode(&value); err != nil {
			return err
		}
		visitErr = errors.Join(visitErr, visit(key.(string), value))
	}
	_, err = d.Token()
	return errors.Join(visitErr, err)
}

// Kinds scans every actions member, discriminator and populated actuating block.
// On error the returned set always flags Unknown.
func Kinds(definition []byte) (set Set, err error) {
	set.Types = map[string]bool{}
	defer func() {
		if err != nil {
			set.Unknown = true
		}
	}()
	if !json.Valid(definition) {
		return set, fmt.Errorf("rule action scan: invalid JSON")
	}
	err = object(definition, func(key string, raw json.RawMessage) error {
		if !strings.EqualFold(key, "actions") {
			return nil
		}
		if len(raw) == 0 || raw[0] != '[' {
			return fmt.Errorf("rule action scan: actions must be an array")
		}
		d := json.NewDecoder(bytes.NewReader(raw))
		if _, err := d.Token(); err != nil {
			return err
		}
		var actionErr error
		for d.More() {
			var action json.RawMessage
			if err := d.Decode(&action); err != nil {
				return err
			}
			typed := false
			if err := object(action, func(key string, value json.RawMessage) error {
				if strings.EqualFold(key, "type") {
					typed = true
					var kind string
					if err := json.Unmarshal(value, &kind); err != nil {
						set.Unknown = true
						return nil
					}
					set.Types[kind] = true
					if !Classified(kind) {
						set.Unknown = true
					}
				}
				for _, kind := range []string{"sendCommand", "httpCall", "publish"} {
					if strings.EqualFold(key, kind) && !bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
						set.Types[kind] = true
					}
				}
				return nil
			}); err != nil {
				actionErr = errors.Join(actionErr, err)
			}
			if !typed {
				set.Unknown = true
			}
		}
		return actionErr
	})
	return set, err
}

// Required returns authorities in stable order; unknown input fails closed.
func Required(set Set) []auth.Authority {
	command, connector := set.Unknown, set.Unknown
	for kind := range set.Types {
		switch kind {
		case "sendCommand":
			command = true
		case "httpCall", "publish":
			connector = true
		case "raiseAlarm":
		default:
			command, connector = true, true
		}
	}
	var out []auth.Authority
	if command {
		out = append(out, auth.CommandWrite)
	}
	if connector {
		out = append(out, auth.ConnectorWrite)
	}
	return out
}
