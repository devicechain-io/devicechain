// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package connectorspec

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSupported(t *testing.T) {
	assert.True(t, Supported("mqtt"))
	assert.False(t, Supported("gcp_pubsub")) // no delivery implementation in this build
	assert.False(t, Supported("carrier_pigeon"))
}

func TestValidateMQTT(t *testing.T) {
	good := []string{
		`{"urls":["tcp://b:1883"],"topic":"t"}`,
		`{"urls":["tcp://b:1883"],"topic":"t","qos":2,"clientId":"c","username":"u"}`,
	}
	for _, c := range good {
		require.NoError(t, ValidateConfig("mqtt", []byte(c)), "config %s should be valid", c)
	}

	bad := []string{
		`{"topic":"t"}`,                                    // no urls
		`{"urls":[],"topic":"t"}`,                          // empty urls
		`{"urls":["  "],"topic":"t"}`,                      // blank url
		`{"urls":["tcp://b:1883"],"topic":""}`,             // empty topic
		`{"urls":["tcp://b:1883"],"topic":"t","qos":3}`,    // qos out of range
		`{"urls":["tcp://b:1883"],"topic":"t","bogus":1}`,  // unknown field (fail-closed)
		`{"urls":["tcp://b:1883"],"topic":"t"} {"x":1}`,    // trailing document
		`{"urls":["tcp://b:1883"],"topic":"t","qos":-1}`,   // qos out of range
		`{"urls":["tcp://b:1883"],"topic":"t","qos":"1"}`,  // wrong type
		`{"urls":["tcp://b:1883"],"topic":"t","urls2":[]}`, // unknown field
	}
	for _, c := range bad {
		require.Error(t, ValidateConfig("mqtt", []byte(c)), "config %s should be rejected", c)
	}
}

func TestValidateConfigUnsupportedType(t *testing.T) {
	require.ErrorIs(t, ValidateConfig("gcp_pubsub", []byte(`{}`)), ErrUnsupportedType)
}

// TestBuildMQTT asserts the authored fields land on the target and the secret becomes the
// broker password.
func TestBuildMQTT(t *testing.T) {
	got, err := Build("mqtt", []byte(`{"urls":["tcp://b:1883","wss://w:443/mqtt"],"topic":"alerts","qos":2,"clientId":"dc","username":"u"}`), "p4ss")
	require.NoError(t, err)
	m, ok := got.(MQTTTarget)
	require.True(t, ok, "an mqtt connector builds an MQTTTarget, got %T", got)
	require.Len(t, m.Brokers, 2)
	assert.Equal(t, "tcp://b:1883", m.Brokers[0].String())
	assert.Equal(t, "wss://w:443/mqtt", m.Brokers[1].String())
	assert.Equal(t, "alerts", m.Topic)
	assert.Equal(t, byte(2), m.QoS)
	assert.Equal(t, "dc", m.ClientID)
	assert.Equal(t, "u", m.Username)
	assert.Equal(t, "p4ss", m.Password)
}

// TestBuildMQTTDefaults: qos defaults to 1 (at-least-once), and an anonymous connector
// carries no password, username or client id.
func TestBuildMQTTDefaults(t *testing.T) {
	got, err := Build("mqtt", []byte(`{"urls":["tcp://b:1883"],"topic":"t"}`), "")
	require.NoError(t, err)
	m := got.(MQTTTarget)
	assert.Equal(t, byte(1), m.QoS)
	assert.Empty(t, m.Password)
	assert.Empty(t, m.Username)
	assert.Empty(t, m.ClientID)
}

func TestSupportedSet(t *testing.T) {
	for _, ok := range []string{"mqtt", "kafka", "aws_sns", "aws_sqs"} {
		assert.True(t, Supported(ok), "%q should have a builder", ok)
	}
	assert.False(t, Supported("gcp_pubsub"))
}

func TestValidateKafka(t *testing.T) {
	good := []string{
		`{"addresses":["b:9092"],"topic":"t"}`,
		`{"addresses":["b:9092"],"topic":"t","clientId":"c","tls":true,"sasl":{"mechanism":"PLAIN","username":"u"}}`,
	}
	for _, c := range good {
		require.NoError(t, ValidateConfig("kafka", []byte(c)), "config %s should be valid", c)
	}
	bad := []string{
		`{"topic":"t"}`,                       // no addresses
		`{"addresses":["b:9092"],"topic":""}`, // empty topic
		`{"addresses":["b:9092"],"topic":"t","sasl":{"mechanism":"WAT","username":"u"}}`, // bad mechanism
		`{"addresses":["b:9092"],"topic":"t","sasl":{"mechanism":"PLAIN"}}`,              // sasl no username
		`{"addresses":["b:9092"],"topic":"t","bogus":1}`,                                 // unknown field
	}
	for _, c := range bad {
		require.Error(t, ValidateConfig("kafka", []byte(c)), "config %s should be rejected", c)
	}
}

func TestBuildKafkaSASL(t *testing.T) {
	got, err := Build("kafka",
		[]byte(`{"addresses":["b:9092","c:9093"],"topic":"t","clientId":"cid","tls":true,"sasl":{"mechanism":"SCRAM-SHA-512","username":"u"}}`), "kpass")
	require.NoError(t, err)
	k, ok := got.(KafkaTarget)
	require.True(t, ok, "a kafka connector builds a KafkaTarget, got %T", got)
	assert.Equal(t, []string{"b:9092", "c:9093"}, k.Seeds)
	assert.Equal(t, "t", k.Topic)
	assert.Equal(t, "cid", k.ClientID)
	assert.True(t, k.TLS)
	require.NotNil(t, k.SASL)
	assert.Equal(t, KafkaSASL{Mechanism: "SCRAM-SHA-512", User: "u", Password: "kpass"}, *k.SASL)
}

// TestBuildKafkaMissingSecret: a SASL block with no sealed secret is a terminal build error.
func TestBuildKafkaMissingSecret(t *testing.T) {
	_, err := Build("kafka",
		[]byte(`{"addresses":["b:9092"],"topic":"t","sasl":{"mechanism":"PLAIN","username":"u"}}`), "")
	require.Error(t, err)
}

func TestValidateAWS(t *testing.T) {
	require.NoError(t, ValidateConfig("aws_sns", []byte(`{"region":"us-east-1","topicArn":"arn:aws:sns:us-east-1:1:t","accessKeyId":"AKIA"}`)))
	require.NoError(t, ValidateConfig("aws_sqs", []byte(`{"region":"us-east-1","url":"https://sqs.example/q","accessKeyId":"AKIA"}`)))
	// Missing required fields / unknown fields rejected.
	require.Error(t, ValidateConfig("aws_sns", []byte(`{"topicArn":"arn:x","accessKeyId":"AKIA"}`)))   // no region
	require.Error(t, ValidateConfig("aws_sns", []byte(`{"region":"us-east-1","accessKeyId":"AKIA"}`))) // no topicArn
	require.Error(t, ValidateConfig("aws_sns", []byte(`{"region":"r","topicArn":"arn:x","bogus":1}`))) // unknown field
	require.Error(t, ValidateConfig("aws_sqs", []byte(`{"region":"us-east-1","accessKeyId":"AKIA"}`))) // no url
	require.Error(t, ValidateConfig("aws_sqs", []byte(`{"region":"r","url":"https://q","x":1}`)))      // unknown field
	// accessKeyId is REQUIRED (fail-closed — ambient AWS credentials are refused, tenant isolation).
	require.Error(t, ValidateConfig("aws_sns", []byte(`{"region":"us-east-1","topicArn":"arn:x"}`)))
	require.Error(t, ValidateConfig("aws_sqs", []byte(`{"region":"us-east-1","url":"https://sqs.example/q"}`)))
}

func TestBuildAWSCredentials(t *testing.T) {
	got, err := Build("aws_sns",
		[]byte(`{"region":"us-east-1","topicArn":"arn:aws:sns:us-east-1:1:t","accessKeyId":"AKIA","endpoint":"https://sns.example"}`), "awssecret")
	require.NoError(t, err)
	s, ok := got.(SNSTarget)
	require.True(t, ok, "an aws_sns connector builds an SNSTarget, got %T", got)
	assert.Equal(t, "us-east-1", s.Region)
	assert.Equal(t, "AKIA", s.AccessKeyID)
	assert.Equal(t, "awssecret", s.SecretAccessKey)
	assert.Equal(t, "arn:aws:sns:us-east-1:1:t", s.TopicARN)
	require.NotNil(t, s.Endpoint)
	assert.Equal(t, "https://sns.example", s.Endpoint.String())

	got, err = Build("aws_sqs", []byte(`{"region":"eu-west-2","url":"https://sqs.example/1/q","accessKeyId":"AKIA"}`), "s")
	require.NoError(t, err)
	q, ok := got.(SQSTarget)
	require.True(t, ok, "an aws_sqs connector builds an SQSTarget, got %T", got)
	assert.Equal(t, "https://sqs.example/1/q", q.QueueURL)
	assert.Nil(t, q.Endpoint, "no override means the regional endpoint")

	// accessKeyId set but NO sealed secret → terminal build error (never a doomed send).
	_, err = Build("aws_sqs", []byte(`{"region":"us-east-1","url":"https://sqs.example/q","accessKeyId":"AKIA"}`), "")
	require.Error(t, err)
}

func TestBuildUnsupportedType(t *testing.T) {
	_, err := Build("gcp_pubsub", []byte(`{}`), "")
	require.ErrorIs(t, err, ErrUnsupportedType)
}
