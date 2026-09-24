// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package connectorspec turns a versioned Connector's {type, config} plus its resolved
// credential into a typed Target the publish package delivers to (ADR-060 Tier 2).
//
// The same parser runs at connector-write time (ValidateConfig, so an author hears about
// a bad destination while still looking at it) and again at dispatch (Build, because a
// stored row may have been written before a rule existed, or past the write path
// entirely). There is one reader of a config, so the two cannot disagree about what a
// destination may be.
//
// What a destination may be is narrow on purpose. Every Target this package returns names
// TCP endpoints only — host and port — because those are the only destinations the
// platform egress guard can judge at connect time. A unix socket, a scheme a client
// library would interpret its own way, a second destination smuggled into one entry
// after a comma: each is refused here, and the publish package's dial refuses anything
// but TCP again underneath.
//
// This package dials nothing and logs nothing; a Target carries the credential in memory
// only.
package connectorspec

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrUnsupportedType is returned for a connector type with no registered target builder
// in this build. It is distinct from "unknown type" (model rejects those at write): a
// type may be a valid, creatable vocabulary member whose client has not shipped yet
// (gcp_pubsub). The dispatch executor maps it to a terminal, dead-lettered outcome —
// recognized but not executable — never a silent drop.
var ErrUnsupportedType = errors.New("connector type has no output generator in this build")

// Target is a fully-validated publish destination plus its credential. It is sealed:
// exactly MQTTTarget, KafkaTarget, SNSTarget and SQSTarget implement it, and the publish
// package switches over those four.
type Target interface{ isTarget() }

// builder parses and validates one connector type's config. It returns a Target without
// its credential; Build attaches the secret and applies the checks that need it. Parsing
// and validation are ONE function so the write path and the dispatch path run the same
// rules.
type builder struct {
	parse  func(config []byte) (Target, error)
	secret func(t Target, secret string) (Target, error)
}

// builders is the registered target set. Adding a target type means adding a parser here
// and a client in the publish package; the `publish` REACT action is unchanged (the type
// selects the transport).
var builders = map[string]builder{
	"mqtt":    {parse: parseMQTT, secret: withMQTTSecret},
	"kafka":   {parse: parseKafka, secret: withKafkaSecret},
	"aws_sns": {parse: parseSNS, secret: withSNSSecret},
	"aws_sqs": {parse: parseSQS, secret: withSQSSecret},
	// gcp_pubsub has no delivery implementation in this build. When it ships it will
	// authenticate with a credential stored on the connector, as the AWS targets do,
	// never with the pod's ambient identity.
}

// Supported reports whether a target builder is registered for connType in this build.
func Supported(connType string) bool {
	_, ok := builders[connType]
	return ok
}

// ValidateConfig checks the per-type config shape for connType. Returns ErrUnsupportedType
// if no builder is registered. It runs exactly the parser Build runs.
func ValidateConfig(connType string, config []byte) error {
	b, ok := builders[connType]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnsupportedType, connType)
	}
	_, err := b.parse(config)
	return err
}

// Build parses and validates connType's stored config and attaches the resolved secret,
// returning the Target the publish package delivers to. It re-validates everything
// ValidateConfig does: a stored row can predate a rule, or have been written past the
// write path. The secret is carried in memory only and is never part of an error.
func Build(connType string, config []byte, secret string) (Target, error) {
	b, ok := builders[connType]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedType, connType)
	}
	t, err := b.parse(config)
	if err != nil {
		return nil, err
	}
	return b.secret(t, secret)
}

// decodeStrict decodes a config object into v, rejecting unknown fields (fail closed: a
// misspelled field is a refusal, never a silently-default value) and trailing data.
func decodeStrict(kind string, config []byte, v any) error {
	dec := json.NewDecoder(strings.NewReader(string(config)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%s config: %w", kind, err)
	}
	if dec.More() {
		return fmt.Errorf("%s config: trailing data after the config object", kind)
	}
	return nil
}
