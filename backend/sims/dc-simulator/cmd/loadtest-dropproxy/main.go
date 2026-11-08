// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// dc-loadtest-dropproxy is the rehearsal's negative control: an MQTT proxy that PUBACKs every
// Nth QoS 1 publish and never forwards it (see package dropproxy). Point dc-loadtest-mqtt's
// --target at it, with the read-back tap on the real broker, and the run must exit 3 naming
// exactly the number of messages this proxy dropped. It prints that number on shutdown and,
// with --report, writes it as JSON so a script can compare the two.
//
// It is a test instrument: plaintext only, no authentication of its own, never to stand between
// real devices and a real broker.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/devicechain-io/dc-simulator/internal/dropproxy"
	"github.com/devicechain-io/dc-simulator/internal/stopsig"
)

func main() {
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339})
	os.Exit(run())
}

func run() int {
	listen := flag.String("listen", "127.0.0.1:1884", "address to listen on (host:port)")
	upstream := flag.String("upstream", "", "the real broker (host:port); required")
	every := flag.Int("every", 100, "acknowledge and drop every Nth QoS 1 publish, counted across all connections")
	report := flag.String("report", "", "write {\"seen\":N,\"dropped\":N} here on shutdown")
	flag.Parse()
	if *upstream == "" {
		log.Error().Msg("--upstream is required")
		return 2
	}
	p, err := dropproxy.Start(*listen, *upstream, *every)
	if err != nil {
		log.Error().Err(err).Msg("start")
		return 2
	}
	log.Info().Str("listen", p.Addr()).Str("upstream", *upstream).Int("every", *every).Msg("drop proxy up")

	ctx, cancel, _ := stopsig.Notify(context.Background())
	defer cancel()
	// The count is rewritten as it moves, not only on a clean shutdown: a proxy that is killed
	// (a signal does not reach a native process from every shell) must still leave its number.
	if *report != "" {
		go func() {
			for {
				if err := writeReport(*report, p); err != nil {
					log.Error().Err(err).Msg("write report")
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(250 * time.Millisecond):
				}
			}
		}()
	}
	p.Serve(ctx)

	log.Info().Int64("seen", p.Seen()).Int64("dropped", p.Dropped()).Msg("drop proxy down")
	if *report != "" {
		if err := writeReport(*report, p); err != nil {
			log.Error().Err(err).Msg("write report")
			return 2
		}
	}
	return 0
}

func writeReport(path string, p *dropproxy.Proxy) error {
	b, err := json.Marshal(map[string]int64{"seen": p.Seen(), "dropped": p.Dropped()})
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
