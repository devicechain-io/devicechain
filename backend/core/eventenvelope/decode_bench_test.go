// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package eventenvelope_test

import (
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-microservice/eventenvelope"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/stretchr/testify/require"
)

// The decode-CPU gate: a member read out of an envelope costs no more than a message read
// today. Every benchmark op is ONE EVENT delivered to a consumer, so ns/op and allocs/op
// compare directly:
//
//	BenchmarkDecodeLegacyMessage     UnmarshalResolvedEvent of the event's own message
//	BenchmarkDecodeEnvelopeMember/kN Parse once per N events (decompress, validate, build the
//	                                 member table) then UnmarshalEnvelopeMember per event
//	BenchmarkDecodeEnvelopeRebuilt   the same through Member (rebuilding the legacy bytes) and
//	                                 UnmarshalResolvedEvent, for a reader with no typed path
//
// The corpus is the loadtest shape the size gate uses; BenchmarkDecode*Heavier use the
// three-reading event with an anchor.
func benchGens() map[string]func(int) *resolvedevent.ResolvedEvent {
	return map[string]func(int) *resolvedevent.ResolvedEvent{"": loadtestResolved, "Heavier": realisticResolved}
}

func BenchmarkDecodeLegacyMessage(b *testing.B) {
	for suffix, gen := range benchGens() {
		b.Run("corpus"+suffix, func(b *testing.B) {
			events := corpusOf(b, 64, gen)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := resolvedevent.UnmarshalResolvedEvent(events[i%64].Bytes); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkDecodeEnvelopeMember(b *testing.B) {
	for suffix, gen := range benchGens() {
		for _, k := range []int{2, 4, 16, 64} {
			b.Run(fmt.Sprintf("corpus%s/k%d", suffix, k), func(b *testing.B) {
				msg, err := eventenvelope.Encode(resolvedHeader(), corpusOf(b, k, gen))
				require.NoError(b, err)
				b.ReportAllocs()
				b.ResetTimer()
				var r *eventenvelope.Reader
				for i := 0; i < b.N; i++ {
					if i%k == 0 {
						if r, err = eventenvelope.Parse(msg); err != nil {
							b.Fatal(err)
						}
					}
					if _, err := resolvedevent.UnmarshalEnvelopeMember(r, i%k); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

func BenchmarkDecodeEnvelopeRebuilt(b *testing.B) {
	for suffix, gen := range benchGens() {
		for _, k := range []int{16} {
			b.Run(fmt.Sprintf("corpus%s/k%d", suffix, k), func(b *testing.B) {
				msg, err := eventenvelope.Encode(resolvedHeader(), corpusOf(b, k, gen))
				require.NoError(b, err)
				b.ReportAllocs()
				b.ResetTimer()
				var r *eventenvelope.Reader
				for i := 0; i < b.N; i++ {
					if i%k == 0 {
						if r, err = eventenvelope.Parse(msg); err != nil {
							b.Fatal(err)
						}
					}
					legacy, err := r.Member(i % k)
					if err != nil {
						b.Fatal(err)
					}
					if _, err := resolvedevent.UnmarshalResolvedEvent(legacy); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// BenchmarkEncode is the producer's side, per event.
func BenchmarkEncode(b *testing.B) {
	for _, k := range []int{4, 16, 64} {
		b.Run(fmt.Sprintf("k%d", k), func(b *testing.B) {
			events := corpusOf(b, k, loadtestResolved)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := eventenvelope.Encode(resolvedHeader(), events); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*k), "ns/event")
		})
	}
}
