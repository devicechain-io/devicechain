// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package connectorspec

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// awsCommon is the shared AWS connection config for the SNS/SQS targets. The credential
// (the AWS secret access key) is NEVER in the config — it is the connector's resolved
// SecretRef, attached by Build.
//
// accessKeyId is REQUIRED (fail-closed, tenant isolation): static per-tenant credentials
// are the only supported AWS auth. We deliberately do NOT fall back to the process's
// ambient AWS credentials (IAM role / IRSA / instance profile) when it is omitted —
// that identity is shared across every tenant in this multi-tenant service, so a tenant
// connector authored with just {region, topicArn} would publish AS THE PLATFORM to an
// arbitrary tenant-authored ARN (a confused deputy, ADR-047). The publish client is built
// from a literal configuration for the same reason: it never reads the pod's environment,
// shared config files or instance metadata. Ambient credentials can be reintroduced later
// behind an explicit operator/instance-level opt-in (self-host, where operator == tenant).
type awsCommon struct {
	// Region is the AWS region. Required.
	Region string `json:"region"`
	// Endpoint overrides the AWS endpoint (e.g. for localstack). Optional. It is dialed
	// through the platform egress guard like every other destination.
	Endpoint string `json:"endpoint,omitempty"`
	// AccessKeyID is the AWS access key id for static credentials (the secret access key is the
	// connector's SecretRef). Required — see the type doc for why ambient credentials are refused.
	AccessKeyID string `json:"accessKeyId"`
}

// AWSTarget is the validated connection half of an SNS or SQS target.
type AWSTarget struct {
	Region          string
	AccessKeyID     string
	SecretAccessKey string
	// Endpoint is the override; nil means the regional AWS endpoint.
	Endpoint *url.URL
}

// SNSTarget is a validated SNS destination.
type SNSTarget struct {
	AWSTarget
	TopicARN string
}

// SQSTarget is a validated SQS destination. QueueURL names the queue in the request; the
// client dials the regional (or overridden) endpoint, never the queue URL's host.
type SQSTarget struct {
	AWSTarget
	QueueURL string
}

func (SNSTarget) isTarget() {}
func (SQSTarget) isTarget() {}

// awsRegion is the shape of an AWS region name. The region becomes part of the endpoint
// hostname the SDK builds, so anything wider than this could steer that hostname.
var awsRegion = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

func (a awsCommon) parse() (AWSTarget, error) {
	t := AWSTarget{Region: a.Region, AccessKeyID: a.AccessKeyID}
	if strings.TrimSpace(a.Region) == "" {
		return t, fmt.Errorf("region is required")
	}
	if len(a.Region) > 32 || !awsRegion.MatchString(a.Region) {
		return t, fmt.Errorf("region %q is not an AWS region name", a.Region)
	}
	if strings.TrimSpace(a.AccessKeyID) == "" {
		return t, fmt.Errorf("accessKeyId is required (ambient AWS credentials are not used — see the connector docs)")
	}
	if a.Endpoint != "" {
		u, err := validHTTPURL(a.Endpoint)
		if err != nil {
			return t, fmt.Errorf("endpoint: %w", err)
		}
		if u.Path != "" && u.Path != "/" {
			return t, fmt.Errorf("endpoint %q must not carry a path", a.Endpoint)
		}
		t.Endpoint = u
	}
	return t, nil
}

// withSecret attaches the secret access key. A connector with an access key id and no
// sealed secret is a terminal misconfiguration — never a valid AWS static credential — so
// it is refused here rather than sent doomed and churning the redelivery cap.
func (t AWSTarget) withSecret(secret string) (AWSTarget, error) {
	if secret == "" {
		return t, fmt.Errorf("no credential is sealed for this connector (a secret access key is required for accessKeyId %q)", t.AccessKeyID)
	}
	t.SecretAccessKey = secret
	return t, nil
}

// snsConfig is the DeviceChain-facing AWS SNS connector config (ADR-060 slice C4c).
type snsConfig struct {
	awsCommon
	// TopicARN is the target SNS topic ARN. Required.
	TopicARN string `json:"topicArn"`
}

func parseSNS(config []byte) (Target, error) {
	var c snsConfig
	if err := decodeStrict("aws_sns", config, &c); err != nil {
		return nil, err
	}
	a, err := c.awsCommon.parse()
	if err != nil {
		return nil, fmt.Errorf("aws_sns config: %w", err)
	}
	if strings.TrimSpace(c.TopicARN) == "" {
		return nil, fmt.Errorf("aws_sns config: topicArn is required")
	}
	if !strings.HasPrefix(c.TopicARN, "arn:") {
		return nil, fmt.Errorf("aws_sns config: topicArn must be an ARN (arn:...)")
	}
	return SNSTarget{AWSTarget: a, TopicARN: c.TopicARN}, nil
}

func withSNSSecret(t Target, secret string) (Target, error) {
	s := t.(SNSTarget)
	a, err := s.AWSTarget.withSecret(secret)
	if err != nil {
		return nil, fmt.Errorf("aws_sns config: %w", err)
	}
	s.AWSTarget = a
	return s, nil
}

// sqsConfig is the DeviceChain-facing AWS SQS connector config (ADR-060 slice C4c).
type sqsConfig struct {
	awsCommon
	// URL is the target SQS queue URL. Required.
	URL string `json:"url"`
}

func parseSQS(config []byte) (Target, error) {
	var c sqsConfig
	if err := decodeStrict("aws_sqs", config, &c); err != nil {
		return nil, err
	}
	a, err := c.awsCommon.parse()
	if err != nil {
		return nil, fmt.Errorf("aws_sqs config: %w", err)
	}
	if strings.TrimSpace(c.URL) == "" {
		return nil, fmt.Errorf("aws_sqs config: url is required")
	}
	if _, err := validHTTPURL(c.URL); err != nil {
		return nil, fmt.Errorf("aws_sqs config: url: %w", err)
	}
	return SQSTarget{AWSTarget: a, QueueURL: c.URL}, nil
}

func withSQSSecret(t Target, secret string) (Target, error) {
	s := t.(SQSTarget)
	a, err := s.AWSTarget.withSecret(secret)
	if err != nil {
		return nil, fmt.Errorf("aws_sqs config: %w", err)
	}
	s.AWSTarget = a
	return s, nil
}
