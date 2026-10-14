// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package downlink

import (
	"context"
	"fmt"
	"testing"

	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The executor is the last gate before the wire: whatever reached it, a raw execute of the Factory
// Reset resource must not leave the process, under any spelling of the path.
func TestRawExecuteOfFactoryResetNeverReachesTheWire(t *testing.T) {
	for _, p := range []string{"/3/0/5", "3/0/5", "/3/0/5/", "/3/00/5", "/3/1/5", "/3/0/5/0"} {
		t.Run(p, func(t *testing.T) {
			conn := &opConn{code: codes.Changed}
			res := NewOps().Execute(context.Background(), conn, CommandExecute, []byte(fmt.Sprintf(`{"path":%q}`, p)))
			require.False(t, res.Success)
			assert.Empty(t, conn.method, "nothing may be sent to the device")
			require.NotNil(t, res.Err)
			assert.Contains(t, *res.Err, "Factory Reset")
		})
	}
}

// A payload naming its path twice is refused for every op, not guessed at.
func TestAmbiguousPathIsRefusedForEveryOp(t *testing.T) {
	for _, name := range []string{CommandRead, CommandWrite, CommandExecute} {
		for _, pl := range []string{`{"path":"/3/0/5","Path":"/3/0/9","value":1}`, `{"path":"/3/0/9","PATH":"/3/0/5","value":1}`} {
			conn := &opConn{code: codes.Changed}
			res := NewOps().Execute(context.Background(), conn, name, []byte(pl))
			require.False(t, res.Success, name+pl)
			assert.Empty(t, conn.method, "nothing may be sent to the device")
			assert.Contains(t, *res.Err, "ambiguous path")
		}
	}
}

func TestOtherExecutesStillReachTheWire(t *testing.T) {
	for _, p := range []string{"/3/0/4", "/5/0/2", "/3/0/50"} {
		conn := &opConn{code: codes.Changed}
		res := NewOps().Execute(context.Background(), conn, CommandExecute, []byte(fmt.Sprintf(`{"path":%q}`, p)))
		require.True(t, res.Success, p)
		assert.Equal(t, p, conn.path)
	}
}
