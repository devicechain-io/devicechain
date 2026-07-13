// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package connectorspec

import (
	"encoding/json"
	"fmt"
	"strings"
)

// awsCommon is the shared AWS connection config for the SNS/SQS outputs. The credential
// (the AWS secret access key) is NEVER in the config — it is the connector's resolved
// SecretRef, injected at BuildOutput time as credentials.secret.
//
// accessKeyId is REQUIRED (fail-closed, tenant isolation): static per-tenant credentials
// are the only supported AWS auth. We deliberately do NOT fall back to the process's
// ambient AWS credentials (IAM role / IRSA / instance profile) when it is omitted —
// that identity is shared across every tenant in this multi-tenant service, so a tenant
// connector authored with just {region, topicArn} would publish AS THE PLATFORM to an
// arbitrary tenant-authored ARN (a confused deputy, ADR-047). This mirrors the gcp_pubsub
// deferral, whose Bento output is ADC-only and thus can't be isolated per tenant. Ambient
// credentials can be reintroduced later behind an explicit operator/instance-level opt-in
// (self-host, where operator == tenant).
type awsCommon struct {
	// Region is the AWS region. Required.
	Region string `json:"region"`
	// Endpoint overrides the AWS endpoint (e.g. for a VPC endpoint or localstack). Optional.
	Endpoint string `json:"endpoint,omitempty"`
	// AccessKeyID is the AWS access key id for static credentials (the secret access key is the
	// connector's SecretRef). Required — see the type doc for why ambient credentials are refused.
	AccessKeyID string `json:"accessKeyId"`
}

// awsCredentials builds the Bento aws `credentials` block from the access key id + resolved
// secret. secret must be non-empty (a credential-requiring shape with no sealed secret is a
// terminal misconfiguration — never a valid AWS static credential — so it is rejected here
// rather than sent doomed and churning the redelivery cap).
func (a awsCommon) awsCredentials(secret string) (map[string]any, error) {
	if secret == "" {
		return nil, fmt.Errorf("no credential is sealed for this connector (a secret access key is required for accessKeyId %q)", a.AccessKeyID)
	}
	return map[string]any{"id": a.AccessKeyID, "secret": secret}, nil
}

func (a awsCommon) validate() error {
	if strings.TrimSpace(a.Region) == "" {
		return fmt.Errorf("region is required")
	}
	if strings.TrimSpace(a.AccessKeyID) == "" {
		return fmt.Errorf("accessKeyId is required (ambient AWS credentials are not used — see the connector docs)")
	}
	return nil
}

// snsConfig is the DeviceChain-facing AWS SNS connector config (ADR-060 slice C4c).
type snsConfig struct {
	awsCommon
	// TopicARN is the target SNS topic ARN. Required.
	TopicARN string `json:"topicArn"`
}

func decodeSNS(config []byte) (*snsConfig, error) {
	dec := json.NewDecoder(strings.NewReader(string(config)))
	dec.DisallowUnknownFields()
	var c snsConfig
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("aws_sns config: %w", err)
	}
	return &c, nil
}

func validateSNS(config []byte) error {
	c, err := decodeSNS(config)
	if err != nil {
		return err
	}
	if err := c.validate(); err != nil {
		return fmt.Errorf("aws_sns config: %w", err)
	}
	if strings.TrimSpace(c.TopicARN) == "" {
		return fmt.Errorf("aws_sns config: topicArn is required")
	}
	return nil
}

func buildSNS(config []byte, secret string) (map[string]any, error) {
	c, err := decodeSNS(config)
	if err != nil {
		return nil, err
	}
	sns := map[string]any{"region": c.Region, "topic_arn": c.TopicARN}
	if c.Endpoint != "" {
		sns["endpoint"] = c.Endpoint
	}
	creds, err := c.awsCredentials(secret)
	if err != nil {
		return nil, fmt.Errorf("aws_sns config: %w", err)
	}
	sns["credentials"] = creds
	return map[string]any{"aws_sns": sns}, nil
}

// sqsConfig is the DeviceChain-facing AWS SQS connector config (ADR-060 slice C4c).
type sqsConfig struct {
	awsCommon
	// URL is the target SQS queue URL. Required.
	URL string `json:"url"`
}

func decodeSQS(config []byte) (*sqsConfig, error) {
	dec := json.NewDecoder(strings.NewReader(string(config)))
	dec.DisallowUnknownFields()
	var c sqsConfig
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("aws_sqs config: %w", err)
	}
	return &c, nil
}

func validateSQS(config []byte) error {
	c, err := decodeSQS(config)
	if err != nil {
		return err
	}
	if err := c.validate(); err != nil {
		return fmt.Errorf("aws_sqs config: %w", err)
	}
	if strings.TrimSpace(c.URL) == "" {
		return fmt.Errorf("aws_sqs config: url is required")
	}
	return nil
}

func buildSQS(config []byte, secret string) (map[string]any, error) {
	c, err := decodeSQS(config)
	if err != nil {
		return nil, err
	}
	sqs := map[string]any{"region": c.Region, "url": c.URL}
	if c.Endpoint != "" {
		sqs["endpoint"] = c.Endpoint
	}
	creds, err := c.awsCredentials(secret)
	if err != nil {
		return nil, fmt.Errorf("aws_sqs config: %w", err)
	}
	sqs["credentials"] = creds
	return map[string]any{"aws_sqs": sqs}, nil
}
