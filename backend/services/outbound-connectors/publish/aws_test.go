// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package publish

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-outbound-connectors/connectorspec"
)

// fakeAWS answers SNS Publish (query protocol, XML) and SQS SendMessage (JSON protocol),
// recording each request.
type fakeAWS struct {
	mu       sync.Mutex
	requests []awsRequest
	hits     atomic.Int32
	respond  func(w http.ResponseWriter, r *http.Request) bool // optional override
}

type awsRequest struct {
	auth, target, body string
}

func (f *fakeAWS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.hits.Add(1)
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.requests = append(f.requests, awsRequest{auth: r.Header.Get("Authorization"), target: r.Header.Get("X-Amz-Target"), body: string(body)})
	f.mu.Unlock()
	if f.respond != nil && f.respond(w, r) {
		return
	}
	if strings.HasPrefix(r.Header.Get("X-Amz-Target"), "AmazonSQS.") {
		w.Header().Set("Content-Type", "application/x-amz-json-1.0")
		_, _ = io.WriteString(w, `{"MessageId":"m-1"}`)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	_, _ = io.WriteString(w, `<PublishResponse xmlns="https://sns.amazonaws.com/doc/2010-03-31/"><PublishResult><MessageId>m-1</MessageId></PublishResult><ResponseMetadata><RequestId>r-1</RequestId></ResponseMetadata></PublishResponse>`)
}

func (f *fakeAWS) seen() []awsRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]awsRequest(nil), f.requests...)
}

func awsTarget(t *testing.T, endpoint string) connectorspec.AWSTarget {
	t.Helper()
	a := connectorspec.AWSTarget{Region: "us-east-1", AccessKeyID: "AKIATENANT", SecretAccessKey: "tenant-secret"}
	if endpoint != "" {
		a.Endpoint = mustURL(t, endpoint)
	}
	return a
}

func snsTarget(t *testing.T, endpoint string) connectorspec.SNSTarget {
	return connectorspec.SNSTarget{AWSTarget: awsTarget(t, endpoint), TopicARN: "arn:aws:sns:us-east-1:1:t"}
}

func sqsTarget(t *testing.T, endpoint string) connectorspec.SQSTarget {
	return connectorspec.SQSTarget{AWSTarget: awsTarget(t, endpoint), QueueURL: "https://sqs.us-east-1.amazonaws.com/1/q"}
}

// The AWS boundary, end to end: an allowed endpoint receives SNS Publish and SQS
// SendMessage signed with the TENANT's credential and carrying the idempotency attribute;
// with no allowance nothing is sent; the metadata address is refused at once; the pod's
// AWS_* environment is not consulted; and a redirect is not followed.
func TestAWSEndpointBoundary(t *testing.T) {
	aws := &fakeAWS{}
	srv := httptest.NewServer(aws)
	t.Cleanup(srv.Close)
	s := NewSender(guardAllowing("127.0.0.1/32"))

	// The pod's environment names another credential and another SNS endpoint. Neither may
	// be used: the second server must see nothing, and the first must see the tenant's key.
	envAWS := &fakeAWS{}
	envSrv := httptest.NewServer(envAWS)
	t.Cleanup(envSrv.Close)
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIAPOD")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "pod-secret")
	t.Setenv("AWS_ENDPOINT_URL", envSrv.URL)
	t.Setenv("AWS_ENDPOINT_URL_SNS", envSrv.URL)
	t.Setenv("AWS_ENDPOINT_URL_SQS", envSrv.URL)

	require.NoError(t, s.Send(sendCtx(t, 5*time.Second), snsTarget(t, srv.URL), []byte(`{"temp":72}`), "idem-sns"))
	require.NoError(t, s.Send(sendCtx(t, 5*time.Second), sqsTarget(t, srv.URL), []byte(`{"temp":73}`), "idem-sqs"))
	got := aws.seen()
	require.Len(t, got, 2)

	assert.Contains(t, got[0].auth, "Credential=AKIATENANT/")
	form, err := url.ParseQuery(got[0].body)
	require.NoError(t, err)
	assert.Equal(t, "Publish", form.Get("Action"))
	assert.Equal(t, "arn:aws:sns:us-east-1:1:t", form.Get("TopicArn"))
	assert.Equal(t, `{"temp":72}`, form.Get("Message"))
	assert.Equal(t, "idempotency_key", form.Get("MessageAttributes.entry.1.Name"))
	assert.Equal(t, "idem-sns", form.Get("MessageAttributes.entry.1.Value.StringValue"))

	assert.Contains(t, got[1].auth, "Credential=AKIATENANT/")
	assert.Equal(t, "AmazonSQS.SendMessage", got[1].target)
	assert.Contains(t, got[1].body, `"QueueUrl":"https://sqs.us-east-1.amazonaws.com/1/q"`)
	assert.Contains(t, got[1].body, `"MessageBody":"{\"temp\":73}"`)
	assert.Contains(t, got[1].body, `"idempotency_key":{"DataType":"String","StringValue":"idem-sqs"}`)

	assert.Equal(t, int32(0), envAWS.hits.Load(), "the pod's AWS_ENDPOINT_URL* must not be used")

	// No allowance: refused before connect, nothing sent.
	for _, target := range []connectorspec.Target{snsTarget(t, srv.URL), sqsTarget(t, srv.URL)} {
		err := NewSender(nil).Send(sendCtx(t, 5*time.Second), target, []byte("x"), "k")
		assert.True(t, isBlocked(err), "%T: want blocked, got %v", target, err)
	}
	assert.Equal(t, int32(2), aws.hits.Load())

	// The metadata address, as an endpoint override: refused at once, not after the SDK's
	// retries.
	start := time.Now()
	err = s.Send(sendCtx(t, 10*time.Second), snsTarget(t, "http://169.254.169.254"), []byte("x"), "k")
	assert.True(t, isBlocked(err), "want blocked, got %v", err)
	assert.Less(t, time.Since(start), time.Second)

	// A redirect is not followed: a signed request re-sent to wherever the response pointed
	// would carry the tenant's signature to a destination nobody authored.
	redirected := &fakeAWS{}
	redirectSrv := httptest.NewServer(redirected)
	t.Cleanup(redirectSrv.Close)
	redirector := &fakeAWS{respond: func(w http.ResponseWriter, r *http.Request) bool {
		http.Redirect(w, r, redirectSrv.URL+r.URL.Path, http.StatusTemporaryRedirect)
		return true
	}}
	redirSrv := httptest.NewServer(redirector)
	t.Cleanup(redirSrv.Close)
	err = s.Send(sendCtx(t, 5*time.Second), snsTarget(t, redirSrv.URL), []byte("x"), "k")
	require.Error(t, err)
	assert.Equal(t, int32(0), redirected.hits.Load(), "a redirect must not be followed")
}

// A response body past the cap is an error, whether it arrives plain or gzip-encoded.
func TestAnOversizedAWSBodyIsRefused(t *testing.T) {
	// Well-formed XML, so the decoder keeps reading rather than stopping at the first byte.
	big := []byte(`<PublishResponse xmlns="https://sns.amazonaws.com/doc/2010-03-31/"><PublishResult><MessageId>` +
		strings.Repeat("a", 5<<20) + `</MessageId></PublishResult></PublishResponse>`)
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write(bytes.Repeat([]byte("a"), 5<<20))
	require.NoError(t, zw.Close())

	for name, respond := range map[string]func(w http.ResponseWriter, r *http.Request) bool{
		"plain": func(w http.ResponseWriter, _ *http.Request) bool {
			w.Header().Set("Content-Type", "text/xml")
			_, _ = w.Write(big)
			return true
		},
		"gzip": func(w http.ResponseWriter, _ *http.Request) bool {
			w.Header().Set("Content-Type", "text/xml")
			w.Header().Set("Content-Encoding", "gzip")
			_, _ = w.Write(gz.Bytes())
			return true
		},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(&fakeAWS{respond: respond})
			t.Cleanup(srv.Close)
			var err error
			grew := heapGrowth(func() {
				err = NewSender(guardAllowing("127.0.0.1/32")).Send(sendCtx(t, 5*time.Second),
					snsTarget(t, srv.URL), []byte("x"), "k")
			})
			require.Error(t, err)
			assert.False(t, isBlocked(err))
			// The SDK retries a failed read, so allow for three capped attempts and their copies.
			assert.Less(t, grew, uint64(24<<20), "the send allocated %d bytes", grew)
			if name == "plain" {
				// Two caps stand here: the connection's (every byte off the wire, headers
				// included) and the body's. Over plain HTTP the connection's trips first.
				capped := false
				for _, c := range []error{errAWSBodyCap, errInboundCap} {
					capped = capped || errors.Is(err, c) || strings.Contains(err.Error(), c.Error())
				}
				assert.True(t, capped, "want a size cap, got %v", err)
			}
		})
	}
}

// The body cap admits exactly the cap and refuses one byte past it.
func TestCappedBody(t *testing.T) {
	read := func(n int) error {
		b := &cappedBody{rc: io.NopCloser(bytes.NewReader(make([]byte, n))), left: 8}
		_, err := io.ReadAll(b)
		return err
	}
	assert.NoError(t, read(8))
	assert.ErrorIs(t, read(9), errAWSBodyCap)
}
