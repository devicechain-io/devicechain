// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package connectorspec turns a versioned Connector's {type, config} + resolved
// credential into the Bento output configuration the publish sink runs (ADR-060 Tier 2).
// It is deliberately Bento-FREE — it only builds a config document — so it can be reused
// at connector-write time and at dispatch without linking the Bento tree into anything
// that only needs to validate a shape. The `publish` package consumes the output this
// produces.
//
// The output config is emitted as a JSON document. JSON is a strict subset of YAML 1.2,
// so Bento's AddOutputYAML parses it directly, and json.Marshal escapes every value —
// so a tenant-supplied URL/topic or a resolved credential can never break out of its
// field or inject additional Bento config (no string interpolation into YAML).
package connectorspec

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// rejectInterpolation guards a value bound for a Bento INTERPOLATED config field (topic,
// key, …) against the Bloblang opener "${!", which Bento would evaluate per message. A
// tenant-authored value must be a literal, never executable Bloblang — the interpolation
// surface is otherwise one transitive import away from exposing functions to tenant input.
func rejectInterpolation(field, value string) error {
	if strings.Contains(value, "${!") {
		return fmt.Errorf("%s must not contain the Bloblang interpolation opener \"${!\"", field)
	}
	return nil
}

// ErrUnsupportedType is returned for a connector type with no registered output builder
// in this build. It is distinct from "unknown type" (model rejects those at write): a
// type may be a valid, creatable vocabulary member whose Bento generator has not shipped
// yet (e.g. kafka before slice C4c). The dispatch executor maps it to a terminal,
// dead-lettered outcome — recognized but not executable — never a silent drop.
var ErrUnsupportedType = errors.New("connector type has no output generator in this build")

// builder is the per-type contract: validate the config shape, and build the Bento
// output config map from the config + resolved secret. Registered in the table below;
// slice C4c adds kafka/aws_sns/aws_sqs/gcp_pubsub entries with no other change.
type builder struct {
	validate func(config []byte) error
	build    func(config []byte, secret string) (map[string]any, error)
}

// builders is the registered output-generator set. Adding a generator is the only change
// needed to ship a new output (SD-4: one `publish` action, the type selects it). C4b
// shipped mqtt; C4c adds kafka + aws_sns + aws_sqs.
var builders = map[string]builder{
	"mqtt":    {validate: validateMQTT, build: buildMQTT},
	"kafka":   {validate: validateKafka, build: buildKafka},
	"aws_sns": {validate: validateSNS, build: buildSNS},
	"aws_sqs": {validate: validateSQS, build: buildSQS},
	// gcp_pubsub is DEFERRED: Bento's gcp_pubsub output authenticates via Application Default
	// Credentials (process-global env / workload identity), with no per-connector credential field —
	// so a per-tenant credential cannot be injected via config without breaking tenant isolation.
	// It needs a credential-injection follow-up before it can ship.
}

// Supported reports whether a Bento output generator is registered for connType in this
// build.
func Supported(connType string) bool {
	_, ok := builders[connType]
	return ok
}

// ValidateConfig checks the per-type config shape for connType. Returns ErrUnsupportedType
// if no generator is registered. Callable at connector-write time (fail early) and at
// dispatch (defense in depth against a forged/corrupt stored config).
func ValidateConfig(connType string, config []byte) error {
	b, ok := builders[connType]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnsupportedType, connType)
	}
	return b.validate(config)
}

// BuildOutput generates the Bento output configuration (a JSON string, which is valid
// YAML for Bento) for connType from its config + resolved secret. It re-validates the
// config (defense in depth: a stored rule could have been forged past the write-time
// gate). The secret is injected into the output config in memory only — it is never
// logged (the publish sink silences Bento's logger and this package logs nothing).
func BuildOutput(connType string, config []byte, secret string) (string, error) {
	b, ok := builders[connType]
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrUnsupportedType, connType)
	}
	if err := b.validate(config); err != nil {
		return "", err
	}
	m, err := b.build(config, secret)
	if err != nil {
		return "", err
	}
	// json.Marshal escapes every value → no field breakout / YAML injection from a
	// tenant-supplied URL/topic or the resolved credential.
	out, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("marshal output config: %w", err)
	}
	// Escape "${" as "$${" so Bento's config env-var substitution (which runs over the raw
	// config before parsing, and which the publish sink additionally neutralizes via an
	// always-unset lookup) leaves every value LITERAL. Without this, a credential or a
	// topic/url containing the literal sequence "${x}" would be silently rewritten to empty
	// (a corrupted send). Bento un-escapes "$${" back to "${" after substitution.
	return strings.ReplaceAll(string(out), "${", "$${"), nil
}
