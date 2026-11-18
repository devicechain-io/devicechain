// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Command scenario-sink receives a scenario's deliveries out of the platform: webhooks on HTTP,
// publishes on MQTT and mail on SMTP. It records them and serves the record on GET /received. The
// harborpulse rig runs it as a pod inside the cluster (see scenariosink).
package main

import (
	"flag"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/devicechain-io/dc-simulator/scenariosink"
)

func main() {
	log.Logger = zerolog.New(os.Stderr).With().Timestamp().Logger()
	httpAddr := flag.String("http", ":8080", "webhook and record address")
	mqttAddr := flag.String("mqtt", ":1883", "MQTT address")
	smtpAddr := flag.String("smtp", ":2525", "SMTP address")
	flag.Parse()

	s := scenariosink.New()
	for _, l := range []struct {
		addr  string
		serve func(net.Listener) error
	}{{*mqttAddr, s.ServeMQTT}, {*smtpAddr, s.ServeSMTP}} {
		ln, err := net.Listen("tcp", l.addr)
		if err != nil {
			log.Fatal().Err(err).Str("addr", l.addr).Msg("listen")
		}
		go func(serve func(net.Listener) error, ln net.Listener) {
			log.Fatal().Err(serve(ln)).Str("addr", ln.Addr().String()).Msg("listener stopped")
		}(l.serve, ln)
	}
	srv := &http.Server{Addr: *httpAddr, Handler: s, ReadHeaderTimeout: 10 * time.Second}
	log.Info().Str("http", *httpAddr).Str("mqtt", *mqttAddr).Str("smtp", *smtpAddr).Msg("scenario sink listening")
	log.Fatal().Err(srv.ListenAndServe()).Msg("http stopped")
}
