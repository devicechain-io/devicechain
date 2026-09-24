// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package publish

import (
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// wsConn adapts a WebSocket connection to the byte stream an MQTT client reads and writes.
// Reads stream across binary messages; each Write is one binary message. It is our own
// because paho's adapter is unexported and dials for itself, which is exactly what this
// package does not allow.
type wsConn struct {
	c *websocket.Conn

	rmu sync.Mutex
	r   io.Reader

	// gorilla permits one concurrent writer; paho writes from more than one goroutine.
	wmu sync.Mutex
}

func newWSConn(c *websocket.Conn) *wsConn { return &wsConn{c: c} }

var errTextFrame = errors.New("publish: the broker sent a text WebSocket message; MQTT is binary")

func (w *wsConn) Read(p []byte) (int, error) {
	w.rmu.Lock()
	defer w.rmu.Unlock()
	for {
		if w.r == nil {
			mt, r, err := w.c.NextReader()
			if err != nil {
				return 0, err
			}
			if mt != websocket.BinaryMessage {
				return 0, errTextFrame
			}
			w.r = r
		}
		n, err := w.r.Read(p)
		if errors.Is(err, io.EOF) {
			w.r = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

func (w *wsConn) Write(p []byte) (int, error) {
	w.wmu.Lock()
	defer w.wmu.Unlock()
	if err := w.c.WriteMessage(websocket.BinaryMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (w *wsConn) Close() error                       { return w.c.Close() }
func (w *wsConn) LocalAddr() net.Addr                { return w.c.LocalAddr() }
func (w *wsConn) RemoteAddr() net.Addr               { return w.c.RemoteAddr() }
func (w *wsConn) SetReadDeadline(t time.Time) error  { return w.c.SetReadDeadline(t) }
func (w *wsConn) SetWriteDeadline(t time.Time) error { return w.c.SetWriteDeadline(t) }

func (w *wsConn) SetDeadline(t time.Time) error {
	if err := w.c.SetReadDeadline(t); err != nil {
		return err
	}
	return w.c.SetWriteDeadline(t)
}
