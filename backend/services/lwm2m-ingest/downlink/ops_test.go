// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package downlink

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// opConn is a mux.Conn that records the one client request Ops issues (method, path, content
// format, body) and returns a scripted response code + body, or a scripted transport error. It
// embeds mux.Conn so any method Ops does not call is nil (a stray call panics — loud).
type opConn struct {
	mux.Conn
	method  string
	path    string
	cf      message.MediaType
	body    []byte
	respErr error
	code    codes.Code
	respBdy []byte
}

func (c *opConn) record(method, path string, cf message.MediaType, payload io.ReadSeeker) (*pool.Message, error) {
	c.method, c.path, c.cf = method, path, cf
	if payload != nil {
		c.body, _ = io.ReadAll(payload)
	}
	if c.respErr != nil {
		return nil, c.respErr
	}
	m := pool.NewMessage(context.Background())
	m.SetCode(c.code)
	if c.respBdy != nil {
		m.SetBody(bytes.NewReader(c.respBdy))
	}
	return m, nil
}

func (c *opConn) Get(_ context.Context, path string, _ ...message.Option) (*pool.Message, error) {
	return c.record("GET", path, 0, nil)
}
func (c *opConn) Put(_ context.Context, path string, cf message.MediaType, payload io.ReadSeeker, _ ...message.Option) (*pool.Message, error) {
	return c.record("PUT", path, cf, payload)
}
func (c *opConn) Post(_ context.Context, path string, cf message.MediaType, payload io.ReadSeeker, _ ...message.Option) (*pool.Message, error) {
	return c.record("POST", path, cf, payload)
}

func TestReadSuccessReturnsBody(t *testing.T) {
	conn := &opConn{code: codes.Content, respBdy: []byte("42.5")}
	res := NewOps().Execute(context.Background(), conn, CommandRead, []byte(`{"path":"/3/0/9"}`))

	assert.Equal(t, "GET", conn.method)
	assert.Equal(t, "/3/0/9", conn.path)
	require.True(t, res.Success)
	require.NotNil(t, res.Payload)
	assert.Equal(t, "42.5", *res.Payload)
	assert.Equal(t, labelRead, res.Op)
}

func TestReadOpaqueBodyIsBase64(t *testing.T) {
	conn := &opConn{code: codes.Content, respBdy: []byte{0xff, 0xfe, 0x00}}
	res := NewOps().Execute(context.Background(), conn, CommandRead, []byte(`{"path":"/3/0/9"}`))
	require.True(t, res.Success)
	assert.Equal(t, "//4A", *res.Payload, "a non-UTF-8 body is returned base64-encoded")
}

func TestReadDeviceErrorMapsFail(t *testing.T) {
	conn := &opConn{code: codes.NotFound}
	res := NewOps().Execute(context.Background(), conn, CommandRead, []byte(`{"path":"/3/0/9"}`))
	assert.False(t, res.Success)
	require.NotNil(t, res.Err)
	assert.Contains(t, *res.Err, "device returned")
}

func TestWriteEncodesStringScalar(t *testing.T) {
	conn := &opConn{code: codes.Changed}
	res := NewOps().Execute(context.Background(), conn, CommandWrite,
		[]byte(`{"path":"/5/0/1","value":"coaps://fw.example/img"}`))
	require.True(t, res.Success)
	assert.Equal(t, "PUT", conn.method)
	assert.Equal(t, "/5/0/1", conn.path)
	assert.Equal(t, message.TextPlain, conn.cf)
	assert.Equal(t, "coaps://fw.example/img", string(conn.body))
}

// A large integer value must be written by its EXACT literal, not lossily floated — json.Number.
func TestWritePreservesLargeIntegerLiteral(t *testing.T) {
	conn := &opConn{code: codes.Changed}
	res := NewOps().Execute(context.Background(), conn, CommandWrite,
		[]byte(`{"path":"/3/0/13","value":9223372036854775807}`))
	require.True(t, res.Success)
	assert.Equal(t, "9223372036854775807", string(conn.body))
}

func TestWriteBooleanIsOneZero(t *testing.T) {
	conn := &opConn{code: codes.Changed}
	res := NewOps().Execute(context.Background(), conn, CommandWrite, []byte(`{"path":"/3/0/99","value":true}`))
	require.True(t, res.Success)
	assert.Equal(t, "1", string(conn.body), "LwM2M text/plain boolean is 1/0")
}

// A non-scalar (object/array) or oversized value is refused BEFORE any wire request.
func TestWriteRejectsNonScalarNoWire(t *testing.T) {
	conn := &opConn{code: codes.Changed}
	res := NewOps().Execute(context.Background(), conn, CommandWrite, []byte(`{"path":"/5/0/1","value":{"a":1}}`))
	assert.False(t, res.Success)
	assert.Empty(t, conn.method, "a non-scalar value must be refused before the wire")
}

func TestWriteRejectsOversizedValueNoWire(t *testing.T) {
	big := `"` + string(bytes.Repeat([]byte("x"), maxWriteValueBytes+1)) + `"`
	conn := &opConn{code: codes.Changed}
	res := NewOps().Execute(context.Background(), conn, CommandWrite, []byte(`{"path":"/5/0/1","value":`+big+`}`))
	assert.False(t, res.Success)
	assert.Empty(t, conn.method, "an oversized value must be refused before the wire")
}

func TestExecutePostsArgs(t *testing.T) {
	conn := &opConn{code: codes.Changed}
	res := NewOps().Execute(context.Background(), conn, CommandExecute, []byte(`{"path":"/5/0/2","args":"0"}`))
	require.True(t, res.Success)
	assert.Equal(t, "POST", conn.method)
	assert.Equal(t, "/5/0/2", conn.path)
	assert.Equal(t, "0", string(conn.body))
}

func TestExecuteBareNoArgs(t *testing.T) {
	conn := &opConn{code: codes.Changed}
	res := NewOps().Execute(context.Background(), conn, CommandExecute, []byte(`{"path":"/5/0/2"}`))
	require.True(t, res.Success)
	assert.Equal(t, "POST", conn.method)
	assert.Empty(t, conn.body)
}

func TestUnknownCommandRefusedAsOther(t *testing.T) {
	conn := &opConn{code: codes.Changed}
	res := NewOps().Execute(context.Background(), conn, "lwm2m.delete", []byte(`{"path":"/3/0/9"}`))
	assert.False(t, res.Success)
	assert.Equal(t, labelOther, res.Op, "an unknown command buckets to the 'other' metric label")
	assert.Empty(t, conn.method, "an unknown command never reaches the wire")
}

func TestInvalidPathRefusedNoWire(t *testing.T) {
	for _, p := range []string{`{"path":"3/0/9"}`, `{"path":"/3/x/9"}`, `{"path":"/3//9"}`, `{"path":"/1/2/3/4/5"}`, `{"path":""}`, `{"path":"/03/0/9"}`, `{"path":"/+3/0/9"}`, `{"path":"/99999/0/9"}`} {
		conn := &opConn{code: codes.Content}
		res := NewOps().Execute(context.Background(), conn, CommandRead, []byte(p))
		assert.False(t, res.Success, "path %q must be refused", p)
		assert.Empty(t, conn.method, "an invalid path %q must not reach the wire", p)
	}
}

func TestTransportTimeoutMapsTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // an already-cancelled ctx models an evicted/timed-out exchange
	conn := &opConn{respErr: errors.New("context canceled")}
	res := NewOps().Execute(ctx, conn, CommandRead, []byte(`{"path":"/3/0/9"}`))
	assert.False(t, res.Success)
	require.NotNil(t, res.Err)
	assert.Contains(t, *res.Err, "timeout")
}

func TestTransportErrorMapsTransport(t *testing.T) {
	conn := &opConn{respErr: errors.New("socket boom")}
	res := NewOps().Execute(context.Background(), conn, CommandWrite, []byte(`{"path":"/5/0/1","value":"x"}`))
	assert.False(t, res.Success)
	assert.Contains(t, *res.Err, "transport error")
}
