// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package streams

import "slices"

// BrokerAdmin is the name of the one principal that creates, updates and deletes streams and
// buckets: the broker administrator. It is not a functional area (nothing in PlatformAreas is
// it, and no Deployment is named for it); its login is minted beside the areas'.
const BrokerAdmin = "broker-admin"

// PlatformAreas are the functional areas that exist, which is every area a declaration here
// or in core/kv may name. A name outside it is a typo, and a typo in a role is a grant to
// nobody that reads as a grant to somebody. mcp and ai-inference are listed although neither
// opens a stream or a bucket, so that a declaration naming them is a statement about a real
// area and the absence of one is a statement too.
var PlatformAreas = []string{
	"ai-inference", "command-delivery", "dashboard-management", "device-management",
	"device-state", "event-management", "event-processing", "event-sources",
	"lwm2m-ingest", "mcp", "notification-management", "outbound-connectors",
	"sparkplug-ingest", "update-management", "user-management",
}

// IsPlatformArea reports whether area is one of PlatformAreas.
func IsPlatformArea(area string) bool { return slices.Contains(PlatformAreas, area) }

// ReadMode is HOW an area reads a stream. It is part of a reader's declaration because the
// four ways differ in what they create on the broker and in what the consumer is called, and
// a grant that did not tell them apart could not be exact.
type ReadMode int

const (
	// ReadDurable is a named pull consumer shared by every replica of the area, called
	// DurableName(instance, area, suffix) (core/messaging). The area creates, updates, fetches
	// from and acknowledges exactly that name.
	ReadDurable ReadMode = iota
	// ReadAckFloor is a durable that acknowledges at a floor rather than per message
	// (messaging.ReaderWithAckFloor). Its name carries a tail, so it is a DIFFERENT consumer
	// from ReadDurable on the same stream, and the area also deletes the legacy
	// ReadDurable-named consumer it replaced.
	ReadAckFloor
	// ReadEphemeral is a read of the stream's PAST by sequence (a batched direct get, or a single
	// get), that exists to replay history: a restart catch-up, a live-gap fill, a rule preview. It
	// creates no consumer on the broker, so it has no consumer name and its grant is a read of the
	// one stream; the stream declares AllowDirect for it. (It used to be an ephemeral consumer
	// whose name the client library chose, which could be granted only as a wildcard over every
	// consumer on the stream.)
	ReadEphemeral
	// ReadLive is a core-NATS subscription on the stream's subjects (messaging.SubscribeLive),
	// serving a GraphQL subscription. It is not a consumer and the stream does not know it is
	// there.
	ReadLive
)

// String names a mode for the messages that mention one.
func (m ReadMode) String() string {
	switch m {
	case ReadDurable:
		return "durable"
	case ReadAckFloor:
		return "ack-floor"
	case ReadEphemeral:
		return "ephemeral"
	case ReadLive:
		return "live"
	default:
		return "unknown"
	}
}

// Consumes reports whether the mode is a durable consumer the area creates on the stream,
// which is what makes the area the stream's administrator today (see Admins) and what the
// max-delivery recorder follows.
func (m ReadMode) Consumes() bool { return m == ReadDurable || m == ReadAckFloor }

// Reader is one area's way of reading one stream.
type Reader struct {
	Area string
	Mode ReadMode
}

// durableReaderAreas are the areas with a consuming reader on any stream but the advisory
// capture: exactly the areas that run a max-delivery recorder (core/messaging starts one for
// any manager with a reader, and refuses to start without it).
func durableReaderAreas() []string {
	var out []string
	for _, s := range All {
		if s.Shape == ShapeAdvisory {
			continue
		}
		for _, r := range s.Readers {
			if r.Mode.Consumes() && !slices.Contains(out, r.Area) {
				out = append(out, r.Area)
			}
		}
	}
	slices.Sort(out)
	return out
}

// RecorderAreas are the areas that run the max-delivery recorder. They are derived rather
// than listed: see Stream.RecorderWrites.
func RecorderAreas() []string { return durableReaderAreas() }

// WritersOf returns the areas that publish into the stream, sorted: its declared Writers,
// plus every recorder area when it RecorderWrites. An undeclared suffix has none.
func WritersOf(suffix string) []string {
	s, found := bySuffix[suffix]
	if !found {
		return nil
	}
	out := slices.Clone(s.Writers)
	if s.RecorderWrites {
		for _, a := range RecorderAreas() {
			if !slices.Contains(out, a) {
				out = append(out, a)
			}
		}
	}
	slices.Sort(out)
	return out
}

// ReadersOf returns every reader of the stream. The advisory capture's are derived: each
// recorder area reads it through its recorder's durable. An undeclared suffix has none.
func ReadersOf(suffix string) []Reader {
	s, found := bySuffix[suffix]
	if !found {
		return nil
	}
	out := slices.Clone(s.Readers)
	if s.Shape == ShapeAdvisory {
		for _, a := range RecorderAreas() {
			out = append(out, Reader{Area: a, Mode: ReadDurable})
		}
	}
	return out
}

// AdminsOf returns the areas that OPEN the stream, sorted: the ones that write it or hold a
// durable consumer on it, whose start therefore needs it to exist.
//
// 🔴 THE NAME IS OLDER THAN THE DISTINCTION. Until the stream-administration step each of these
// areas also created and reconfigured the stream at its own start, and "administers" was true.
// It is not any more: the right to create, update and delete a stream belongs to the broker
// administrator alone (BrokerAdmin), and an area listed here only VERIFIES the stream
// (messaging's verifyStream) and fails its start, naming what differs, when it is missing or
// wrong. What the list still decides is which streams the administrator creates for an
// installation (those a deployed area opens) and which areas the replication check expects to
// have started them. An ephemeral or live reader opens nothing, so it is not here.
func AdminsOf(suffix string) []string {
	var out []string
	for _, a := range WritersOf(suffix) {
		if !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	for _, r := range ReadersOf(suffix) {
		if r.Mode.Consumes() && !slices.Contains(out, r.Area) {
			out = append(out, r.Area)
		}
	}
	slices.Sort(out)
	return out
}

// Admins is AdminsOf for this declaration. It is what the deployment predicate of the
// replication check reads: an area that administers a stream is one whose start creates it.
func (s Stream) Admins() []string { return AdminsOf(s.Suffix) }

// Writes reports whether area may publish into the stream.
func Writes(suffix, area string) bool { return slices.Contains(WritersOf(suffix), area) }

// ReadsAs reports whether area is declared to read the stream in mode.
func ReadsAs(suffix, area string, mode ReadMode) bool {
	return slices.Contains(ReadersOf(suffix), Reader{Area: area, Mode: mode})
}

// Administers reports whether area OPENS the stream (see AdminsOf). It does not mean the area may
// create or change it: only the broker administrator may.
func Administers(suffix, area string) bool { return slices.Contains(AdminsOf(suffix), area) }
