// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// A decode failure's text reaches the tenant as written (it is built from the reason, the
// expression path, the function name and the offset), bounded like every record's, and the reason
// is the number the decoder's writer set.
func TestADecodeFailureRecordKeepsItsReasonAndItsText(t *testing.T) {
	text := "decode_failed: eval_error: sections[0].outputs[0].expr: i16be: offset 4 is past the end of the frame"
	b, err := proto.Marshal(&resolvedevent.PFailedEvent{Reason: resolvedevent.FailureReason_DecodeFailed, Service: "device-management", Error: text})
	require.NoError(t, err)
	rec, err := ParseFailedRecord(b)
	require.NoError(t, err)
	require.EqualValues(t, resolvedevent.FailureReason_DecodeFailed, rec.Reason)
	require.Equal(t, text, rec.Error)

	long, err := proto.Marshal(&resolvedevent.PFailedEvent{Reason: resolvedevent.FailureReason_DecodeFailed, Service: "device-management",
		Error: "decode_failed: " + strings.Repeat("x", 4000)})
	require.NoError(t, err)
	rec, err = ParseFailedRecord(long)
	require.NoError(t, err)
	require.LessOrEqual(t, len(rec.Error), 512)
	require.True(t, strings.HasPrefix(rec.Error, "decode_failed: "))
}
