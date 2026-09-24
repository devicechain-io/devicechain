// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package publish

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	snstypes "github.com/aws/aws-sdk-go-v2/service/sns/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/devicechain-io/dc-outbound-connectors/connectorspec"
)

// maxAWSResponseBytes caps one AWS response body. A Publish or SendMessage answer is a few
// hundred bytes; an endpoint override pointing somewhere hostile could otherwise stream
// without end into the SDK's decoder.
//
// It is deliberately well below maxInboundBytesPerConn, the cap on everything the
// connection receives: the body cap is the one that fires on an oversized response, with
// its own error, and the connection's stays the backstop for headers and TLS records.
const maxAWSResponseBytes = 256 << 10

var errAWSBodyCap = errors.New("publish: the AWS endpoint sent a response larger than a send can need")

// awsConfig builds the literal configuration for one SNS or SQS send.
//
// It is LITERAL on purpose: config.LoadDefaultConfig is never called, so nothing is read
// from the pod — no AWS_* environment variables (including AWS_ENDPOINT_URL*), no shared
// config or credentials files, no instance metadata, no STS. The only credential is the
// connector's; the only destination is the regional endpoint or the connector's override,
// dialed through the guarded dial. The returned transport is closed by the caller.
func (s *Sender) awsConfig(log *dialLog, a connectorspec.AWSTarget) (aws.Config, *http.Transport) {
	tr := s.guard.Transport()
	tr.DialContext = s.dial(log)
	tr.Proxy = nil
	// A gzip response would otherwise be inflated by the transport underneath the body cap.
	tr.DisableCompression = true
	cfg := aws.Config{
		Region: a.Region,
		Credentials: aws.NewCredentialsCache(
			credentials.NewStaticCredentialsProvider(a.AccessKeyID, a.SecretAccessKey, "")),
		HTTPClient: &http.Client{
			Transport: bodyCap{next: tr},
			// A redirect would re-send a signed request to wherever the response pointed.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
	if a.Endpoint != nil {
		cfg.BaseEndpoint = aws.String(a.Endpoint.String())
	}
	return cfg, tr
}

func (s *Sender) sendSNS(ctx context.Context, log *dialLog, t connectorspec.SNSTarget, payload []byte, idempotencyKey string) error {
	cfg, tr := s.awsConfig(log, t.AWSTarget)
	defer tr.CloseIdleConnections()
	in := &sns.PublishInput{TopicArn: aws.String(t.TopicARN), Message: aws.String(string(payload))}
	if idempotencyKey != "" {
		in.MessageAttributes = map[string]snstypes.MessageAttributeValue{
			"idempotency_key": {DataType: aws.String("String"), StringValue: aws.String(idempotencyKey)},
		}
	}
	_, err := sns.NewFromConfig(cfg).Publish(ctx, in)
	return err
}

// sendSQS sends one message. The queue URL names the queue in the request body; the client
// dials the regional endpoint (or the override), never the queue URL's host.
func (s *Sender) sendSQS(ctx context.Context, log *dialLog, t connectorspec.SQSTarget, payload []byte, idempotencyKey string) error {
	cfg, tr := s.awsConfig(log, t.AWSTarget)
	defer tr.CloseIdleConnections()
	in := &sqs.SendMessageInput{QueueUrl: aws.String(t.QueueURL), MessageBody: aws.String(string(payload))}
	if idempotencyKey != "" {
		in.MessageAttributes = map[string]sqstypes.MessageAttributeValue{
			"idempotency_key": {DataType: aws.String("String"), StringValue: aws.String(idempotencyKey)},
		}
	}
	_, err := sqs.NewFromConfig(cfg).SendMessage(ctx, in)
	return err
}

// bodyCap limits every response body to maxAWSResponseBytes, erroring past it.
type bodyCap struct{ next http.RoundTripper }

func (b bodyCap) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := b.next.RoundTrip(r)
	if err != nil || resp.Body == nil {
		return resp, err
	}
	resp.Body = &cappedBody{rc: resp.Body, left: maxAWSResponseBytes}
	return resp, nil
}

type cappedBody struct {
	rc   io.ReadCloser
	left int64
}

func (c *cappedBody) Read(p []byte) (int, error) {
	if c.left <= 0 {
		// One more byte decides it: exactly the cap is allowed, past it is not.
		var one [1]byte
		n, err := c.rc.Read(one[:])
		if n > 0 {
			return 0, errAWSBodyCap
		}
		return 0, err
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.rc.Read(p)
	c.left -= int64(n)
	return n, err
}

func (c *cappedBody) Close() error { return c.rc.Close() }
