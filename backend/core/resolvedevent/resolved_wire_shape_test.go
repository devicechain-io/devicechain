// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package resolvedevent

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/eventtime"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

// The resolved event is written by hand (resolved_wire.go), not through the generated
// struct, so a field added to any message it writes would not reach the wire unless the
// writer learns it — and the equivalence tests would not notice, because their oracle
// builds the generated struct from the same model fields the writer knows.
//
// The test below therefore takes its field list from nowhere but the generated
// DESCRIPTOR. It populates every field the descriptor declares, at every level — the
// envelope, its anchors and memberships, and the measurements payload carried inside it —
// encodes that with the protobuf runtime, and sends it through the hop a resolved event
// takes: the decoder every consumer reads it with, then the writer. Whatever comes out
// must decode to the message that went in. A new field the model, the decoder or the
// writer does not carry is lost on that hop, and the test names it. Nothing here lists
// fields, so there is nothing to keep in step with the .proto.
//
// The payload is the one field the descriptor cannot describe: it is `bytes`, holding a
// message whose type the envelope's event_type selects. The test fixes those two fields to
// a measurement event carrying a fully populated PResolvedMeasurementsPayload, the one
// payload the writer writes itself, and compares the payload as a decoded message.
//
// (event-sources/proto/measurements_wire_shape_test.go is the same guard for the
// unresolved measurements payload. The populate and compare helpers are repeated rather
// than shared because the two live in different modules and are test code.)

// resolvedHop is the path under test: decode as a consumer does, re-encode with the writer.
func resolvedHop(encoded []byte) ([]byte, error) {
	decoded, err := UnmarshalResolvedEvent(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return MarshalResolvedEventAs(decoded, eventtime.WireCompact)
}

// populatedString is the value every string field is given. It is valid UTF-8 and an RFC
// 3339 instant in the canonical UTC form the writer produces, so a string field that is a
// time survives the decoder's parse and the writer's re-format unchanged. n distinguishes
// list elements.
func populatedString(n int) string {
	return time.Date(2026, 10, 1, 12, 0, n, 123456789, time.UTC).Format(time.RFC3339Nano)
}

// populatedScalar is a non-default value of fd's kind, so proto3 cannot omit it. Signed
// values are negative. That does NOT make this guard see a writer that forgets to
// sign-extend an int32: the runtime decodes an int32 from the low 32 bits of its varint, so
// a zero-extended -1 decodes as -1 all the same. Only the byte-for-byte equivalence tests
// (resolved_wire_test.go) see that.
func populatedScalar(t *testing.T, fd protoreflect.FieldDescriptor, n int) protoreflect.Value {
	t.Helper()
	switch fd.Kind() {
	case protoreflect.BoolKind:
		return protoreflect.ValueOfBool(n%2 == 0)
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return protoreflect.ValueOfInt32(-int32(n + 1))
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return protoreflect.ValueOfInt64(-int64(n + 1))
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return protoreflect.ValueOfUint32(uint32(n + 1))
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return protoreflect.ValueOfUint64(uint64(n + 1))
	case protoreflect.FloatKind:
		return protoreflect.ValueOfFloat32(float32(n) + 1.5)
	case protoreflect.DoubleKind:
		return protoreflect.ValueOfFloat64(float64(n) + 1.5)
	case protoreflect.StringKind:
		return protoreflect.ValueOfString(populatedString(n))
	case protoreflect.BytesKind:
		return protoreflect.ValueOfBytes([]byte{byte(n + 1)})
	case protoreflect.EnumKind:
		values := fd.Enum().Values()
		return protoreflect.ValueOfEnum(values.Get(values.Len() - 1).Number())
	}
	t.Fatalf("field %s is of kind %v, which this test cannot populate: teach populatedScalar it",
		fd.FullName(), fd.Kind())
	return protoreflect.Value{}
}

// isStringFormOfACount reports whether fd is the RFC 3339 string form of an instant whose
// usual form is a count of nanoseconds in a sibling field: "<name>_time" beside an sfixed64
// "<name>". The two forms are alternatives, written one or the other and never both, and a
// reader takes the count when both are present; so a message populated with both cannot
// survive the hop, and must not be asked to. The string form has its own shapes in the
// equivalence test (an instant past what the count holds). Read off the descriptor, like
// everything else here.
func isStringFormOfACount(fd protoreflect.FieldDescriptor) bool {
	name := string(fd.Name())
	if fd.Kind() != protoreflect.StringKind || !strings.HasSuffix(name, "_time") {
		return false
	}
	count := fd.ContainingMessage().Fields().ByName(protoreflect.Name(strings.TrimSuffix(name, "_time")))
	return count != nil && count.Kind() == protoreflect.Sfixed64Kind
}

// populate sets every field m's descriptor declares, recursively, with two elements in
// each list, so a writer that wrote only the first would differ. The string form of a
// counted instant is left unset (isStringFormOfACount).
func populate(t *testing.T, m protoreflect.Message, depth int) {
	t.Helper()
	if depth > 4 {
		t.Fatalf("message %s nests deeper than this test populates", m.Descriptor().FullName())
	}
	fields := m.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if isStringFormOfACount(fd) {
			continue
		}
		switch {
		case fd.IsMap():
			t.Fatalf("field %s is a map, which this test does not populate: a map's order is not "+
				"the runtime's to fix, so the writer's equivalence tests need rethinking too", fd.FullName())
		case fd.IsList():
			list := m.Mutable(fd).List()
			for n := 0; n < 2; n++ {
				if fd.Kind() == protoreflect.MessageKind {
					el := list.NewElement()
					populate(t, el.Message(), depth+1)
					list.Append(el)
				} else {
					list.Append(populatedScalar(t, fd, n))
				}
			}
		case fd.Kind() == protoreflect.MessageKind || fd.Kind() == protoreflect.GroupKind:
			populate(t, m.Mutable(fd).Message(), depth+1)
		default:
			m.Set(fd, populatedScalar(t, fd, 0))
		}
		if !m.Has(fd) {
			t.Fatalf("field %s was not populated: this test would prove nothing about it", fd.FullName())
		}
	}
}

// lostFields lists the fields of want that got lost or changed, by full name, recursing
// into list elements.
func lostFields(want, got protoreflect.Message) []string {
	var lost []string
	fields := want.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if !want.Has(fd) {
			continue // set aside by the caller, as the payload is
		}
		if !got.Has(fd) {
			lost = append(lost, string(fd.FullName())+" (absent)")
			continue
		}
		if fd.IsList() && fd.Kind() == protoreflect.MessageKind {
			wl, gl := want.Get(fd).List(), got.Get(fd).List()
			if wl.Len() != gl.Len() {
				lost = append(lost, fmt.Sprintf("%s (%d elements, want %d)", fd.FullName(), gl.Len(), wl.Len()))
				continue
			}
			for j := 0; j < wl.Len(); j++ {
				lost = append(lost, lostFields(wl.Get(j).Message(), gl.Get(j).Message())...)
			}
			continue
		}
		if !want.Get(fd).Equal(got.Get(fd)) {
			lost = append(lost, string(fd.FullName())+" (changed)")
		}
	}
	return lost
}

// resolvedHopLoss populates a resolved measurement event of file's descriptors fully,
// sends it through hop, and returns an error naming each field the hop did not carry, or
// nil when it carried them all.
func resolvedHopLoss(t *testing.T, file protoreflect.FileDescriptor, hop func([]byte) ([]byte, error)) error {
	t.Helper()
	eventMD := file.Messages().ByName("PResolvedEvent")
	payloadMD := file.Messages().ByName("PResolvedMeasurementsPayload")
	if eventMD == nil || payloadMD == nil {
		t.Fatal("PResolvedEvent or PResolvedMeasurementsPayload is not in the file descriptor")
	}
	eventTypeFD := eventMD.Fields().ByName("event_type")
	payloadFD := eventMD.Fields().ByName("payload")
	if eventTypeFD == nil || payloadFD == nil {
		t.Fatal("PResolvedEvent no longer declares event_type and payload: rethink this test")
	}

	wantPayload := dynamicpb.NewMessage(payloadMD)
	populate(t, wantPayload, 0)
	payloadBytes, err := proto.Marshal(wantPayload)
	if err != nil {
		t.Fatalf("the protobuf runtime cannot encode the populated payload: %v", err)
	}
	want := dynamicpb.NewMessage(eventMD)
	populate(t, want, 0)
	want.Set(eventTypeFD, protoreflect.ValueOfInt64(int64(eventtype.Measurement)))
	want.Set(payloadFD, protoreflect.ValueOfBytes(payloadBytes))

	in, err := proto.Marshal(want)
	if err != nil {
		t.Fatalf("the protobuf runtime cannot encode the populated event: %v", err)
	}
	out, err := hop(in)
	if err != nil {
		return fmt.Errorf("the hop refused a fully populated event: %w", err)
	}
	got := dynamicpb.NewMessage(eventMD)
	if err := proto.Unmarshal(out, got); err != nil {
		return fmt.Errorf("the hop's output does not decode: %w", err)
	}
	gotPayload := dynamicpb.NewMessage(payloadMD)
	if err := proto.Unmarshal(got.Get(payloadFD).Bytes(), gotPayload); err != nil {
		return fmt.Errorf("the hop's payload does not decode: %w", err)
	}
	// The payload is compared as a message, not as bytes, so the envelope comparison
	// leaves it out.
	want.Clear(payloadFD)
	got.Clear(payloadFD)

	var lost []string
	if !proto.Equal(want, got) {
		lost = append(lost, lostFields(want, got)...)
		if len(lost) == 0 {
			lost = append(lost, "(the envelopes differ, but no single field names the difference)")
		}
	}
	if !proto.Equal(wantPayload, gotPayload) {
		before := len(lost)
		lost = append(lost, lostFields(wantPayload, gotPayload)...)
		if len(lost) == before {
			lost = append(lost, "(the payloads differ, but no single field names the difference)")
		}
	}
	if len(lost) == 0 {
		return nil
	}
	lost = slices.Compact(lost) // a field lost from every list element is named once
	return fmt.Errorf("decoding then writing a resolved event loses what the .proto declares:\n  %s\n"+
		"when a field is added to the .proto, the model, the decoder (marshal.go) and the direct "+
		"writer (resolved_wire.go) must all carry it", strings.Join(lost, "\n  "))
}

// TestResolvedEventWireCarriesEveryDeclaredField is the guard against the writer falling
// behind the .proto: see the comment at the top of this file.
func TestResolvedEventWireCarriesEveryDeclaredField(t *testing.T) {
	if err := resolvedHopLoss(t, File_resolvedevent_dc_resolved_events_proto, resolvedHop); err != nil {
		t.Fatal(err)
	}
}

// resolvedWireMessages is every message the writer writes itself.
var resolvedWireMessages = []string{
	"PResolvedEvent", "PResolvedAnchor", "PScopeMembership",
	"PResolvedMeasurementsPayload", "PResolvedMeasurementsEntry", "PResolvedMeasurementEntry",
}

// sizedByTheirOwnTag are the fields numbered past 15 that the writer sizes with
// protowire.SizeTag rather than tagSize (disabledSize in resolved_wire.go). A field joins
// this list only together with that sizing: TestResolvedEventWireMatchesProtoMarshal's
// "every field" case then fails as a sized-versus-written mismatch if it is sized wrong.
var sizedByTheirOwnTag = map[string]bool{
	"io.devicechain.devicemanagement.PResolvedEvent.source_device_disabled": true,
	"io.devicechain.devicemanagement.PResolvedEvent.occurred":               true,
	"io.devicechain.devicemanagement.PResolvedEvent.processed":              true,
}

// fieldsBeyondOneByteTags names every field of the writer's messages whose number does
// not encode, with its wire type, in one byte, and that the writer does not size by its own
// tag.
func fieldsBeyondOneByteTags(t *testing.T, file protoreflect.FileDescriptor) []string {
	t.Helper()
	var beyond []string
	for _, name := range resolvedWireMessages {
		md := file.Messages().ByName(protoreflect.Name(name))
		if md == nil {
			t.Fatalf("%s is not in the file descriptor", name)
		}
		for i := 0; i < md.Fields().Len(); i++ {
			fd := md.Fields().Get(i)
			if protowire.SizeTag(fd.Number()) != tagSize && !sizedByTheirOwnTag[string(fd.FullName())] {
				beyond = append(beyond, fmt.Sprintf("%s = %d", fd.FullName(), fd.Number()))
			}
		}
	}
	return beyond
}

// TestResolvedWireFieldNumbersFitTheTagSize pins the writer's tagSize assumption: every
// tag it writes is sized as one byte, which holds only for field numbers up to 15. A field
// numbered 16 or above in any message the writer writes fails here, by name, rather than
// as a sized-versus-written mismatch once the writer is taught it.
func TestResolvedWireFieldNumbersFitTheTagSize(t *testing.T) {
	if beyond := fieldsBeyondOneByteTags(t, File_resolvedevent_dc_resolved_events_proto); len(beyond) > 0 {
		t.Fatalf("the direct writer (resolved_wire.go) sizes every tag as %d byte, which these fields "+
			"do not fit; size their tags with protowire.SizeTag:\n  %s", tagSize, strings.Join(beyond, "\n  "))
	}

	// The negative control: field 19 added to each message must be named. (19, because
	// PResolvedEvent already holds fields 16 to 18, each sized by its own tag.)
	for _, message := range resolvedWireMessages {
		fdp := protodesc.ToFileDescriptorProto(File_resolvedevent_dc_resolved_events_proto)
		for _, m := range fdp.MessageType {
			if m.GetName() == message {
				m.Field = append(m.Field, &descriptorpb.FieldDescriptorProto{
					Name: proto.String("nineteenth"), JsonName: proto.String("nineteenth"), Number: proto.Int32(19),
					Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
					Type:  descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
				})
			}
		}
		file, err := protodesc.NewFile(fdp, nil)
		if err != nil {
			t.Fatalf("the extended descriptor does not build: %v", err)
		}
		beyond := fieldsBeyondOneByteTags(t, file)
		if len(beyond) != 1 || !strings.Contains(beyond[0], message+".nineteenth") {
			t.Errorf("field 19 added to %s: named %v, want exactly it", message, beyond)
		}
	}
}

// TestResolvedEventFieldGuardFailsOnANewField is that guard's negative control. For each
// message the writer writes, it adds a field to a copy of the generated descriptor —
// exactly what editing the .proto and regenerating would do — and requires the guard to
// fail, naming the field. A guard that cannot fail on the change it exists for would pass
// forever.
func TestResolvedEventFieldGuardFailsOnANewField(t *testing.T) {
	for _, message := range resolvedWireMessages {
		t.Run(message, func(t *testing.T) {
			fdp := protodesc.ToFileDescriptorProto(File_resolvedevent_dc_resolved_events_proto)
			var target *descriptorpb.DescriptorProto
			for _, m := range fdp.MessageType {
				if m.GetName() == message {
					target = m
				}
			}
			if target == nil {
				t.Fatalf("%s is not in the generated file descriptor", message)
			}
			target.Field = append(target.Field, &descriptorpb.FieldDescriptorProto{
				Name:     proto.String("added_later"),
				JsonName: proto.String("addedLater"),
				Number:   proto.Int32(99),
				Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
			})
			file, err := protodesc.NewFile(fdp, nil)
			if err != nil {
				t.Fatalf("the extended descriptor does not build: %v", err)
			}

			err = resolvedHopLoss(t, file, resolvedHop)
			if err == nil {
				t.Fatal("the field guard passed with a field the writer does not know: it cannot catch drift")
			}
			if !strings.Contains(err.Error(), message+".added_later") {
				t.Fatalf("the field guard failed without naming the new field:\n%v", err)
			}
			// And it names nothing else: a guard that also blames fields the hop carried
			// sends whoever reads it after the wrong ones.
			for _, line := range strings.Split(err.Error(), "\n") {
				if strings.HasPrefix(line, "  ") && !strings.Contains(line, ".added_later") {
					t.Fatalf("the field guard named a field the hop carried, %q:\n%v", strings.TrimSpace(line), err)
				}
			}
			t.Logf("the guard's refusal, as whoever adds a field will see it:\n%v", err)
		})
	}
}
