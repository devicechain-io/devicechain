// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// sitepulse-record makes a board recording of a sitepulse run whose device plane the
// simulator played (dcctl sim run <name> --play-devices --publish-log <file>).
//
//	sitepulse-record watch --handshake <sim.json> --out alarms.ndjson
//	    during the run: read the alarm list every few seconds, append each answer
//
//	sitepulse-record build --handshake <sim.json> --publish-log published.ndjson \
//	    --alarms alarms.ndjson --from <RFC 3339> --to <RFC 3339> --board <sp-dashboard.json> \
//	    --board-commit <sha> --out recording.json
//	    after it: read the window's measurements and positions back from the platform,
//	    check every row against the publish log, and write the recording
//
// The recording package says why there are two steps. Like the simulator, this is an
// untrusted external client: it signs in as the sim's own tenant identity and reads
// through the tenant GraphQL APIs.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/devicechain-io/dc-microservice/userclient"
	"github.com/devicechain-io/dc-simulator/recording"
	"github.com/devicechain-io/dc-simulator/sim"
)

func main() {
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339})
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: sitepulse-record watch|build [flags]")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var err error
	switch os.Args[1] {
	case "watch":
		err = watch(ctx, os.Args[2:])
	case "build":
		err = build(ctx, os.Args[2:])
	default:
		err = fmt.Errorf("unknown step %q: want watch or build", os.Args[1])
	}
	if err != nil {
		log.Fatal().Err(err).Msg("sitepulse-record")
	}
}

func session(handshakePath string) (*sim.Handshake, *userclient.TenantSession, error) {
	if handshakePath == "" {
		return nil, nil, fmt.Errorf("--handshake is required")
	}
	hs, err := sim.LoadHandshake(handshakePath)
	if err != nil {
		return nil, nil, err
	}
	httpc := &http.Client{Timeout: 60 * time.Second}
	return hs, userclient.NewTenantSession(httpc, hs.Endpoints.UserGraphQL, hs.SimEmail, hs.SimPassword, hs.Tenant), nil
}

func watch(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("watch", flag.ExitOnError)
	handshakePath := fs.String("handshake", "", "the sim's handshake file (dcctl sim create wrote it)")
	out := fs.String("out", "alarms.ndjson", "append each answer of the alarm list to this NDJSON file")
	every := fs.Duration("every", 5*time.Second, "how often to read the alarm list")
	_ = fs.Parse(args)
	hs, s, err := session(*handshakePath)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(*out, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	log.Info().Str("tenant", hs.Tenant).Dur("every", *every).Str("out", *out).Msg("watching the alarm list")
	return recording.Watch(ctx, s, hs.Endpoints.DeviceMgmtGraphQL, *every, f, func(err error) {
		log.Warn().Err(err).Msg("alarm watch")
	})
}

// eventMgmtGraphQL is event-management's HTTP GraphQL endpoint, from the graphql-ws one
// the handshake carries (the same path, over http(s)).
func eventMgmtGraphQL(hs *sim.Handshake) string {
	u := hs.Endpoints.EventMgmtWS
	switch {
	case strings.HasPrefix(u, "wss://"):
		return "https://" + strings.TrimPrefix(u, "wss://")
	case strings.HasPrefix(u, "ws://"):
		return "http://" + strings.TrimPrefix(u, "ws://")
	}
	return u
}

func build(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("build", flag.ExitOnError)
	handshakePath := fs.String("handshake", "", "the sim's handshake file (dcctl sim create wrote it)")
	publishLog := fs.String("publish-log", "", "the device plane's publish log (dcctl sim run --publish-log)")
	alarms := fs.String("alarms", "", "the alarm watch (sitepulse-record watch --out)")
	from := fs.String("from", "", "the window's start, RFC 3339")
	to := fs.String("to", "", "the window's end, RFC 3339")
	liveFrom := fs.String("showcase-from", "", "where the showcase starts (a chapter), RFC 3339; the window before it is its history")
	historyEvery := fs.Duration("history-every", time.Minute, "keep one reading per device and measurement in each step this long before the showcase start (0 keeps all)")
	fullRate := fs.String("full-rate", recording.DefaultFullRate, "comma-separated measurements whose history is never thinned: the ones the board aggregates (a stat card's Avg or Peak)")
	board := fs.String("board", "", "the board definition the recording is for (frontend/testdata/sim-dashboards/sp-dashboard.json)")
	boardPath := fs.String("board-path", "frontend/testdata/sim-dashboards/sp-dashboard.json", "the board's path in the repository, as the recording names it")
	boardCommit := fs.String("board-commit", "", "the full commit id the board file is at")
	gitSha := fs.String("git-sha", "", "the commit the simulator that played the devices was built from")
	platformVersion := fs.String("platform-version", "", "the platform version the run was recorded against")
	out := fs.String("out", "", "write the recording here")
	_ = fs.Parse(args)

	parse := func(name, v string) (time.Time, error) {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			return time.Time{}, fmt.Errorf("--%s: %w", name, err)
		}
		return t, nil
	}
	var o recording.Options
	var err error
	if o.From, err = parse("from", *from); err != nil {
		return err
	}
	if o.To, err = parse("to", *to); err != nil {
		return err
	}
	if o.LiveFrom, err = parse("showcase-from", *liveFrom); err != nil {
		return err
	}
	if *out == "" || *platformVersion == "" {
		return fmt.Errorf("--out and --platform-version are required")
	}
	hs, s, err := session(*handshakePath)
	if err != nil {
		return err
	}
	read := func(path string) ([]byte, error) {
		if path == "" {
			return nil, fmt.Errorf("a required file was not named")
		}
		return os.ReadFile(path)
	}
	if o.PublishLog, err = read(*publishLog); err != nil {
		return err
	}
	if o.Alarms, err = read(*alarms); err != nil {
		return err
	}
	if o.BoardBytes, err = read(*board); err != nil {
		return err
	}
	o.PublishLogName, o.AlarmsName = "published.ndjson", "alarms.ndjson"
	o.HistoryEvery = *historyEvery
	if *fullRate != "" {
		o.FullRate = strings.Split(*fullRate, ",")
	}
	o.BoardPath, o.BoardCommit, o.GitSha, o.PlatformVersion = *boardPath, *boardCommit, *gitSha, *platformVersion
	o.Instance, o.Tenant = hs.InstanceId, hs.Tenant
	if hs.ManifestId != "sitepulse" {
		return fmt.Errorf("this records a sitepulse run; the sim is %q", hs.ManifestId)
	}
	m := sim.NewSitepulse(hs.Seed, sim.Load{}).Manifest()
	o.Devices = m.Expand(m.Seed)
	o.Declared = recording.DeclaredByType(m)

	rec, rep, err := recording.Build(ctx, &recording.GraphQLPlatform{Session: s, EventURL: eventMgmtGraphQL(hs)}, o)
	if err != nil {
		return err
	}
	f, err := os.Create(*out)
	if err != nil {
		return err
	}
	if err := recording.Write(f, rec); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	log.Info().
		Int("platformRows", rep.PlatformRows).Int("keptRows", rep.KeptRows).
		Int("platformFixes", rep.PlatformFixes).Int("keptFixes", rep.KeptFixes).
		Int("publishedInWindow", rep.PublishedInWindow).Int("missingOnServer", rep.MissingOnServer).
		Int("alarmAnswers", rep.AlarmAnswers).Int("alarmEvents", rep.AlarmEvents).
		Str("out", *out).Msg("recording written; every row checked against the publish log")
	if rep.MissingOnServer > 0 {
		log.Warn().Int("missing", rep.MissingOnServer).Msg("events the ingress accepted are not in the store for this window")
	}
	return nil
}
