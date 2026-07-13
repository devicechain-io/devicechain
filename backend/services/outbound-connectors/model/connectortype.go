// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"errors"
	"fmt"
	"sort"
)

// ConnectorType is a registered outbound-connector kind. It is DeviceChain's stable
// token for a target, decoupled from the underlying Bento output name (the C4b/C4c
// generator maps a type onto its Bento output), so the type value is API-stable even
// if a component's registered name changes. A `publish` action's ConnectorRef resolves
// to a Connector whose Type is one of these.
type ConnectorType string

const (
	// ConnectorTypeMQTT publishes to an MQTT broker topic.
	ConnectorTypeMQTT ConnectorType = "mqtt"
	// ConnectorTypeKafka publishes to a Kafka topic.
	ConnectorTypeKafka ConnectorType = "kafka"
	// ConnectorTypeAWSSNS publishes to an AWS SNS topic.
	ConnectorTypeAWSSNS ConnectorType = "aws_sns"
	// ConnectorTypeAWSSQS publishes to an AWS SQS queue.
	ConnectorTypeAWSSQS ConnectorType = "aws_sqs"
	// ConnectorTypeGCPPubSub publishes to a GCP Pub/Sub topic.
	ConnectorTypeGCPPubSub ConnectorType = "gcp_pubsub"
)

// registeredConnectorTypes is the v1 target set (ADR-060 §5). Adding a fast-follow
// output (RabbitMQ/Slack/…) adds an entry here plus its generator — no schema/action
// change (SD-4: one generic `publish`, the type selects the transport).
var registeredConnectorTypes = map[ConnectorType]struct{}{
	ConnectorTypeMQTT:      {},
	ConnectorTypeKafka:     {},
	ConnectorTypeAWSSNS:    {},
	ConnectorTypeAWSSQS:    {},
	ConnectorTypeGCPPubSub: {},
}

// ErrUnknownConnectorType is returned when a create/update names a type outside the
// registered vocabulary. Failing closed at write keeps an unroutable connector — one
// no generator could ever execute — out of the store entirely.
var ErrUnknownConnectorType = errors.New("connector type is not one of the registered outbound targets")

// ValidConnectorType reports whether s names a registered connector type.
func ValidConnectorType(s string) bool {
	_, ok := registeredConnectorTypes[ConnectorType(s)]
	return ok
}

// ConnectorTypes returns the registered connector-type vocabulary, sorted. The GraphQL
// surface exposes it so the console can offer a picker rather than a free-text field.
func ConnectorTypes() []string {
	out := make([]string, 0, len(registeredConnectorTypes))
	for t := range registeredConnectorTypes {
		out = append(out, string(t))
	}
	sort.Strings(out)
	return out
}

// validateConnectorType rejects a type outside the registered vocabulary.
func validateConnectorType(t string) error {
	if !ValidConnectorType(t) {
		return fmt.Errorf("%w: %q (known: %v)", ErrUnknownConnectorType, t, ConnectorTypes())
	}
	return nil
}
