// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Command brokersmoke checks a RUNNING DeviceChain broker from outside (see internal/smoke for what
// is checked and why). It is the instrument behind hack/broker-image-smoke.sh, which runs the
// built image in a container, and of the G16 S0 spike. It is not shipped: ko builds the module's
// root package only.
//
//	brokersmoke prepare -dir D [-san NAME ...] [-conf-dir P]
//	    mint a server PKI, device certificates, the callout keys and a nats.conf into D
//	brokersmoke check -dir D -nats URL -mqtt HOST:PORT [-expect-request=false]
//	    run the callout responder and the Go clients against the broker; exit 1 on any failure
//	brokersmoke respond -dir D -nats URL [-log FILE]
//	    run only the responder, one JSON line per authorization request, until interrupted
//	brokersmoke connect -dir D -mqtt HOST:PORT -user U [-cert NAME] [-tls12]
//	    one Go paho connect + QoS 1 publish; exit 0 when the broker accepted it
//	brokersmoke probe -dir D -mqtt HOST:PORT
//	    print whether the listener sent a CertificateRequest
//	brokersmoke hold -dir D -nats URL -mqtt HOST:PORT -n N -ready FILE -stop FILE [-timeout 5m]
//	    hold N token-device sessions (and the callout responder) open until FILE -stop exists, then
//	    print "lost=<n>" (sessions dropped meanwhile) and exit 0
//	brokersmoke rotate -dir D
//	    re-issue the broker's leaf under the same CA and rewrite server.crt/server.key in place,
//	    as a cert-manager renewal does; print the new serial
//	brokersmoke serial -dir D -mqtt HOST:PORT
//	    print the serial of the leaf the MQTT listener presents
package main

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/devicechain-io/dc-broker/nats-server/internal/smoke"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: brokersmoke prepare|check|respond|connect|probe|hold|rotate|serial [flags]")
		os.Exit(2)
	}
	cmds := map[string]func([]string) error{
		"prepare": cmdPrepare, "check": cmdCheck, "respond": cmdRespond, "connect": cmdConnect, "probe": cmdProbe,
		"hold": cmdHold, "rotate": cmdRotate, "serial": cmdSerial,
	}
	cmd, ok := cmds[os.Args[1]]
	if !ok {
		fmt.Fprintf(os.Stderr, "brokersmoke: unknown subcommand %q\n", os.Args[1])
		os.Exit(2)
	}
	if err := cmd(os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "brokersmoke:", err)
		os.Exit(1)
	}
}

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func cmdPrepare(args []string) error {
	fs := flag.NewFlagSet("prepare", flag.ExitOnError)
	dir := fs.String("dir", "", "directory to write into (required; created if absent)")
	var o smoke.PrepareOptions
	fs.StringVar(&o.ConfDir, "conf-dir", "", "the directory as the BROKER sees it, for paths inside nats.conf (default: -dir, absolute)")
	var sans stringList
	fs.Var(&sans, "san", "a name the broker's certificate is valid for (repeatable; default localhost and 127.0.0.1)")
	_ = fs.Parse(args)
	o.SANs = sans
	return smoke.Prepare(*dir, o)
}

func cmdCheck(args []string) error {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	dir := fs.String("dir", "", "the prepare directory (required)")
	url := fs.String("nats", "tls://127.0.0.1:4222", "broker client URL")
	addr := fs.String("mqtt", "127.0.0.1:1883", "broker MQTT address")
	expect := fs.Bool("expect-request", true, "the listener is expected to request client certificates (false for the upstream image, the negative control)")
	_ = fs.Parse(args)
	if *dir == "" {
		return errors.New("check: -dir is required")
	}
	steps, err := smoke.Check(*dir, *url, *addr, *expect)
	if err != nil {
		return err
	}
	for _, s := range steps {
		if s.Err != nil {
			fmt.Printf("FAIL  %s: %v\n", s.Name, s.Err)
		} else {
			fmt.Printf("ok    %s: %s\n", s.Name, s.Note)
		}
	}
	if n := smoke.Failed(steps); n > 0 {
		return fmt.Errorf("%d of %d checks failed (expect-request=%v)", n, len(steps), *expect)
	}
	fmt.Printf("all %d checks passed (expect-request=%v)\n", len(steps), *expect)
	return nil
}

func cmdRespond(args []string) error {
	fs := flag.NewFlagSet("respond", flag.ExitOnError)
	dir := fs.String("dir", "", "the prepare directory (required)")
	url := fs.String("nats", "tls://127.0.0.1:4222", "broker client URL")
	logFile := fs.String("log", "", "append one JSON line per request here (default stdout)")
	_ = fs.Parse(args)
	sec, err := smoke.LoadSecrets(*dir)
	if err != nil {
		return err
	}
	nc, err := smoke.ConnectService(*dir, *url, sec)
	if err != nil {
		return fmt.Errorf("service connect: %w", err)
	}
	defer nc.Close()
	out := os.Stdout
	if *logFile != "" {
		f, err := os.OpenFile(*logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		defer f.Close()
		out = f
	}
	r, err := smoke.StartResponder(nc, sec)
	if err != nil {
		return err
	}
	var mu sync.Mutex
	r.Sink = func(rec smoke.Record) {
		mu.Lock()
		defer mu.Unlock()
		b, _ := json.Marshal(rec)
		fmt.Fprintln(out, string(b))
	}
	fmt.Fprintln(os.Stderr, "brokersmoke: responder ready")
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	return nil
}

func cmdConnect(args []string) error {
	fs := flag.NewFlagSet("connect", flag.ExitOnError)
	dir := fs.String("dir", "", "the prepare directory (required)")
	addr := fs.String("mqtt", "127.0.0.1:1883", "broker MQTT address")
	user := fs.String("user", smoke.TokenUser, "MQTT username")
	cert := fs.String("cert", "", "present <dir>/<cert>.crt and .key (device, big, wrongkey)")
	id := fs.String("client-id", "", "MQTT client id (default: random)")
	tls12 := fs.Bool("tls12", false, "cap the handshake at TLS 1.2, as many device TLS stacks are")
	_ = fs.Parse(args)
	cfg, err := smoke.ClientTLS(*dir, *cert)
	if err != nil {
		return err
	}
	if *tls12 {
		cfg.MaxVersion = tls.VersionTLS12
	}
	if *id == "" {
		*id = smoke.RandomID()
	}
	return smoke.Connect(*addr, *user, *id, cfg)
}

func cmdProbe(args []string) error {
	fs := flag.NewFlagSet("probe", flag.ExitOnError)
	dir := fs.String("dir", "", "the prepare directory (required)")
	addr := fs.String("mqtt", "127.0.0.1:1883", "broker MQTT address")
	_ = fs.Parse(args)
	req, err := smoke.Probe(*dir, *addr)
	if err != nil {
		return err
	}
	fmt.Printf("certificate_request=%v\n", req)
	return nil
}

func cmdHold(args []string) error {
	fs := flag.NewFlagSet("hold", flag.ExitOnError)
	dir := fs.String("dir", "", "the prepare directory (required)")
	url := fs.String("nats", "tls://127.0.0.1:4222", "broker client URL")
	addr := fs.String("mqtt", "127.0.0.1:1883", "broker MQTT address")
	n := fs.Int("n", 50, "sessions to hold")
	ready := fs.String("ready", "", "create this file once every session is open (required)")
	stop := fs.String("stop", "", "finish when this file exists (required)")
	timeout := fs.Duration("timeout", 5*time.Minute, "give up waiting for -stop after this long")
	_ = fs.Parse(args)
	if *ready == "" || *stop == "" {
		return errors.New("hold: -ready and -stop are required")
	}
	f, err := smoke.HoldFleet(*dir, *url, *addr, *n)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := os.WriteFile(*ready, []byte("ready\n"), 0o644); err != nil {
		return err
	}
	deadline := time.Now().Add(*timeout)
	for {
		if _, err := os.Stat(*stop); err == nil {
			break
		}
		if time.Now().After(deadline) {
			return errors.New("hold: timed out waiting for the stop file")
		}
		time.Sleep(200 * time.Millisecond)
	}
	fmt.Printf("lost=%d\n", f.Lost())
	if f.Lost() > 0 {
		fmt.Printf("first_loss=%s\n", f.FirstLoss())
		return nil
	}
	if err := f.Publish(); err != nil {
		return fmt.Errorf("a surviving session does not work: %w", err)
	}
	return nil
}

func cmdRotate(args []string) error {
	fs := flag.NewFlagSet("rotate", flag.ExitOnError)
	dir := fs.String("dir", "", "the prepare directory (required)")
	_ = fs.Parse(args)
	serial, err := smoke.RotateServerCert(*dir)
	if err != nil {
		return err
	}
	fmt.Println(serial)
	return nil
}

func cmdSerial(args []string) error {
	fs := flag.NewFlagSet("serial", flag.ExitOnError)
	dir := fs.String("dir", "", "the prepare directory (required)")
	addr := fs.String("mqtt", "127.0.0.1:1883", "broker MQTT address")
	_ = fs.Parse(args)
	serial, err := smoke.ServedSerial(*dir, *addr)
	if err != nil {
		return err
	}
	fmt.Println(serial)
	return nil
}
