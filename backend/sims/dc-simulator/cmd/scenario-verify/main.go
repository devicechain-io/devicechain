// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// scenario-verify judges one compressed day of a running harborpulse sim against the platform.
// It reads the sim's scenario clock from its control API (/status), waits for the next day it
// can watch from the start of its first judged beat, polls the alarm list through that day, then
// reads the commands, readings and rule previews back over the tenant GraphQL APIs as the sim's
// own identity, and judges each invariant:
//
//	flood-escalates             one flood alarm per gauge, raised at MINOR, escalating in place
//	                            (to CRITICAL on East Quay only) and clearing
//	pump-commanded              start-pump SUCCESSFUL, the pump running and its well falling, the
//	                            well never reaching the pump's own float
//	derate-obeyed               derate SUCCESSFUL, the cap reported at 11 kW, connector-hot cleared
//	                            before the long session ends
//	air-lock-only-east          pump-air-lock for the East Quay pump only, inside its window
//	correlation-fires           previewRule of quay-flooding fires for East Quay only
//	correlation-control-is-red  the same preview with a count no quay reaches is judged red
//	outage-blast-radius         light-silent / charger-offline for exactly the East Quay devices
//	backfill-stored             every buffered charger reading stored at the time it was taken
//
// Exit status: 0 every invariant holds, 1 an invariant failed, 2 no verdict (the sim was not
// reachable, its day never started or restarted mid-watch, the polls left a gap, or a read failed).
// Like the load harnesses it is an untrusted external client: no service token, no database.
package main

import (
	"context"
	"flag"
	"net/http"
	"os"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/devicechain-io/dc-microservice/httptransport"
	"github.com/devicechain-io/dc-simulator/internal/stopsig"
	"github.com/devicechain-io/dc-simulator/scenarioverify"
	"github.com/devicechain-io/dc-simulator/sim"
)

func main() {
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339})

	handshakePath := flag.String("handshake", os.Getenv("DC_SIM_HANDSHAKE"),
		"path to the handshake JSON written by 'dcctl sim create' (or set DC_SIM_HANDSHAKE)")
	simURL := flag.String("sim", "http://127.0.0.1:8090", "the running sim's control API, which carries the scenario clock")
	poll := flag.Duration("poll", 2*time.Second, "how often the alarm list is read through the watch")
	settle := flag.Duration("settle", 20*time.Second, "how long the watch runs past its last beat, so the last clear is seen")
	startTimeout := flag.Duration("start-timeout", 5*time.Minute, "how long to wait for the scenario's day to start")
	day := flag.Int("day", -1, "the sim day to judge (-1: the first whose watch has not begun)")
	reportPath := flag.String("report", "", "write the JSON report to this path (the human one goes to stderr)")
	flag.Parse()

	if *handshakePath == "" {
		log.Error().Msg("--handshake (or DC_SIM_HANDSHAKE) is required")
		os.Exit(scenarioverify.ExitNoVerdict)
	}
	hs, err := sim.LoadHandshake(*handshakePath)
	if err != nil {
		log.Error().Err(err).Msg("load handshake")
		os.Exit(scenarioverify.ExitNoVerdict)
	}
	httpc := &http.Client{Timeout: 60 * time.Second}
	if hs.HostHeader != "" {
		httpc.Transport = httptransport.PresentHost(httptransport.New(), hs.HostHeader)
	}
	// The sim's own control API is local and needs no Host override.
	simHTTP := &http.Client{Timeout: 10 * time.Second}

	ctx, cancel, stop := stopsig.Notify(context.Background())
	defer cancel()

	report, err := scenarioverify.Run(ctx, scenarioverify.Config{
		Handshake: hs, SimURL: *simURL, HTTP: httpc, StatusHTTP: simHTTP,
		Poll: *poll, Settle: *settle, StartTimeout: *startTimeout, Day: *day,
	})
	if err != nil {
		if code, ok := stop.ExitCode(); ok {
			log.Warn().Err(err).Int("exit", code).Msg("stopped by a signal: no verdict")
			os.Exit(code)
		}
		log.Error().Err(err).Msg("scenario-verify stopped: no verdict")
		os.Exit(scenarioverify.ExitNoVerdict)
	}
	if *reportPath != "" {
		data, jerr := report.JSON()
		if jerr == nil {
			jerr = os.WriteFile(*reportPath, append(data, '\n'), 0o644)
		}
		if jerr != nil {
			log.Error().Err(jerr).Str("path", *reportPath).Msg("write report")
			os.Exit(scenarioverify.ExitNoVerdict)
		}
	}
	os.Stderr.WriteString("\n" + report.Human() + "\n")
	os.Exit(report.ExitCode())
}
