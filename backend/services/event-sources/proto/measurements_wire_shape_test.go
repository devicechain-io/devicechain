// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/eventtime"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

// The measurements payload is written by hand (measurements_wire.go), not through the
// generated struct, so a field added to PUnresolvedMeasurementsPayload or
// PUnresolvedMeasurementsEntry would not reach the wire unless the writer learns it — and
// the equivalence tests would not notice, because their oracle builds the generated
// struct from the same two fields the writer knows.
//
// The test below therefore takes its field list from nowhere but the generated
// DESCRIPTOR. It populates every field the descriptor declares, at every level, encodes
// that with the protobuf runtime, and sends it through the hop a published payload takes:
// the decoder device-management reads it with, then the writer. Whatever comes out must
// decode to the message that went in. A new field the model, the decoder or the writer
// does not carry is lost on that hop, and the test names it. Nothing here lists fields, so
// there is nothing to keep in step with the .proto.

// measurementsHop is the path under test: decode as the consumer does, re-encode with
// the direct writer.
func measurementsHop(encoded []byte) ([]byte, error) {
	decoded, err := UnmarshalPayloadForMeasurementsEvent(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return MarshalPayloadForMeasurementsEvent(decoded, eventtime.WireCompact)
}

// populatedTime is the value every string field is given. It is valid UTF-8, a usable
// map key, and an RFC 3339 instant in the canonical UTC form the writer produces, so a
// string field that is a time survives the decoder's parse and the writer's re-format
// unchanged. n distinguishes map keys and list elements.
func populatedTime(n int) string {
	return time.Date(2026, 10, 1, 12, 0, n, 123456789, time.UTC).Format(time.RFC3339Nano)
}

// populatedScalar is a non-default value of fd's kind, so proto3 cannot omit it.
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
		return protoreflect.ValueOfString(populatedTime(n))
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

// populate sets every field m's descriptor declares, recursively: two elements in each
// list and two entries in each map, so a writer that wrote only the first would differ.
// A map value is never equal to its key, so a writer that swapped them would differ too.
// The string form of a counted instant is left unset (isStringFormOfACount).
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
			mp := m.Mutable(fd).Map()
			for n := 0; n < 2; n++ {
				key := populatedScalar(t, fd.MapKey(), n).MapKey()
				if fd.MapValue().Kind() == protoreflect.MessageKind {
					populate(t, mp.Mutable(key).Message(), depth+1)
				} else {
					mp.Set(key, populatedScalar(t, fd.MapValue(), n+2))
				}
			}
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
// into list and map elements.
func lostFields(want, got protoreflect.Message) []string {
	var lost []string
	fields := want.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
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

// hopLoss populates a message of md fully, sends it through hop, and returns an error
// naming each field the hop did not carry, or nil when it carried them all.
func hopLoss(t *testing.T, md protoreflect.MessageDescriptor, hop func([]byte) ([]byte, error)) error {
	t.Helper()
	want := dynamicpb.NewMessage(md)
	populate(t, want, 0)
	in, err := proto.Marshal(want)
	if err != nil {
		t.Fatalf("the protobuf runtime cannot encode the populated message: %v", err)
	}
	out, err := hop(in)
	if err != nil {
		return fmt.Errorf("the hop refused a fully populated payload: %w", err)
	}
	got := dynamicpb.NewMessage(md)
	if err := proto.Unmarshal(out, got); err != nil {
		return fmt.Errorf("the hop's output does not decode: %w", err)
	}
	if proto.Equal(want, got) {
		return nil
	}
	lost := lostFields(want, got)
	if len(lost) == 0 {
		lost = []string{"(the messages differ, but no single field names the difference)"}
	}
	return fmt.Errorf("decoding then writing a measurements payload loses what %s declares:\n  %s\n"+
		"when a field is added to the .proto, the model, the decoder (marshal.go) and the direct "+
		"writer (measurements_wire.go) must all carry it", md.FullName(), strings.Join(lost, "\n  "))
}

// TestMeasurementsPayloadWireCarriesEveryDeclaredField is the guard against the writer
// falling behind the .proto: see the comment at the top of this file.
func TestMeasurementsPayloadWireCarriesEveryDeclaredField(t *testing.T) {
	if err := hopLoss(t, (&PUnresolvedMeasurementsPayload{}).ProtoReflect().Descriptor(), measurementsHop); err != nil {
		t.Fatal(err)
	}
}

// TestMeasurementsPayloadFieldGuardFailsOnANewField is that guard's negative control. It
// adds a field to PUnresolvedMeasurementsEntry in a copy of the generated descriptor —
// exactly what editing the .proto and regenerating would do — and requires the guard to
// fail, naming the field. A guard that cannot fail on the change it exists for would pass
// forever.
func TestMeasurementsPayloadFieldGuardFailsOnANewField(t *testing.T) {
	fdp := protodesc.ToFileDescriptorProto(File_proto_dc_event_sources_events_proto)
	var entry *descriptorpb.DescriptorProto
	for _, m := range fdp.MessageType {
		if m.GetName() == "PUnresolvedMeasurementsEntry" {
			entry = m
		}
	}
	if entry == nil {
		t.Fatal("PUnresolvedMeasurementsEntry is not in the generated file descriptor")
	}
	entry.Field = append(entry.Field, &descriptorpb.FieldDescriptorProto{
		Name:     proto.String("classifier"),
		JsonName: proto.String("classifier"),
		Number:   proto.Int32(99),
		Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
		Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
	})
	file, err := protodesc.NewFile(fdp, nil)
	if err != nil {
		t.Fatalf("the extended descriptor does not build: %v", err)
	}
	md := file.Messages().ByName("PUnresolvedMeasurementsPayload")

	err = hopLoss(t, md, measurementsHop)
	if err == nil {
		t.Fatal("the field guard passed with a field the writer does not know: it cannot catch drift")
	}
	if !strings.Contains(err.Error(), "PUnresolvedMeasurementsEntry.classifier") {
		t.Fatalf("the field guard failed without naming the new field:\n%v", err)
	}
	t.Logf("the guard's refusal, as whoever adds a field will see it:\n%v", err)
}
