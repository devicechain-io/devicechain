// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"bufio"
	"bytes"
	"flag"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protodelim"
	gproto "google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/devicechain-io/dc-microservice/entityevent"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
)

var updateDescriptors = flag.Bool("update-descriptors", false,
	"rewrite testdata/frozen_descriptors.bin from the compiled-in descriptors")

const frozenDescriptorsPath = "testdata/frozen_descriptors.bin"

// The device-management events used to be one proto file; they now span three (the
// resolved-event and entity-event contracts live in core). The wire is the contract, so the
// three files' descriptors, taken together, must stay equal to a frozen snapshot: a field
// renumbered, retyped or dropped in any of them changes what is on the wire for every consumer
// without any one package's own tests noticing. An INTENTIONAL, wire-compatible change is
// recorded by rerunning with -update-descriptors and reviewing the testdata diff.
func compiledDescriptors() []*descriptorpb.FileDescriptorProto {
	files := []protoreflect.FileDescriptor{
		(&resolvedevent.PResolvedEvent{}).ProtoReflect().Descriptor().ParentFile(),
		(&entityevent.PEntityDeletedEvent{}).ProtoReflect().Descriptor().ParentFile(),
		(&PAlarmStateChangeEvent{}).ProtoReflect().Descriptor().ParentFile(),
	}
	out := make([]*descriptorpb.FileDescriptorProto, 0, len(files))
	for _, f := range files {
		out = append(out, protodesc.ToFileDescriptorProto(f))
	}
	return out
}

func TestDescriptorsEqualFrozenSet(t *testing.T) {
	got := compiledDescriptors()
	if *updateDescriptors {
		require.NoError(t, os.MkdirAll(filepath.Dir(frozenDescriptorsPath), 0o755))
		var buf bytes.Buffer
		for _, d := range got {
			_, err := protodelim.MarshalTo(&buf, d)
			require.NoError(t, err)
		}
		require.NoError(t, os.WriteFile(frozenDescriptorsPath, buf.Bytes(), 0o644))
	}
	raw, err := os.ReadFile(frozenDescriptorsPath)
	require.NoError(t, err)
	r := bufio.NewReader(bytes.NewReader(raw))
	var want []*descriptorpb.FileDescriptorProto
	for {
		d := &descriptorpb.FileDescriptorProto{}
		if err := protodelim.UnmarshalFrom(r, d); err == io.EOF {
			break
		} else {
			require.NoError(t, err)
		}
		want = append(want, d)
	}
	require.Len(t, want, len(got))
	for i := range got {
		require.True(t, gproto.Equal(want[i], got[i]),
			"descriptor for %s drifted from %s", got[i].GetName(), frozenDescriptorsPath)
	}
}
