// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Command ota-device is the payload of the reference updatable device: the "firmware". It
// connects to the instance's MQTT broker as one device, follows the update protocol on the
// device-desired and device-reports topics, and replaces itself, A/B style, under the ota-boot
// launcher.
//
//	ota-device describe          print this image's identity and exit (the preflight asks this)
//	ota-device [flags]           run
package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"devicechain.io/examples/ota-device/internal/boot"
	"devicechain.io/examples/ota-device/internal/buildinfo"
	"devicechain.io/examples/ota-device/internal/device"
	"devicechain.io/examples/ota-device/internal/verify"
)

func main() {
	// `describe` comes first and touches nothing: no flags, no files, no network. The preflight
	// runs an image that has only just been verified, with an empty environment.
	if len(os.Args) == 2 && os.Args[1] == "describe" {
		_ = json.NewEncoder(os.Stdout).Encode(verify.Identity{Product: buildinfo.Product, Version: buildinfo.Version})
		return
	}
	os.Exit(run())
}

// tlsScheme lists the broker URL schemes that carry TLS.
var tlsScheme = map[string]bool{"ssl": true, "tls": true, "mqtts": true, "tcps": true, "wss": true}

type mqttSender struct {
	c     mqtt.Client
	topic string
}

func (s *mqttSender) Send(payload []byte) error {
	if !s.c.IsConnectionOpen() {
		return errors.New("not connected")
	}
	t := s.c.Publish(s.topic, 1, false, payload)
	if !t.WaitTimeout(15 * time.Second) {
		return errors.New("publish timed out")
	}
	return t.Error()
}

func run() int {
	var (
		root       = flag.String("root", os.Getenv("OTA_ROOT"), "state directory (shared with ota-boot)")
		broker     = flag.String("broker", "", "MQTT broker URL, e.g. ssl://mqtt.example.com:8883")
		ca         = flag.String("ca", "", "instance CA certificate (PEM) for the broker")
		serverName = flag.String("tls-server-name", "", "host name to verify the broker certificate against, when it differs from the broker host")
		instance   = flag.String("instance", "", "instance id")
		tenant     = flag.String("tenant", "", "tenant id")
		deviceTok  = flag.String("device", "", "device token")
		credID     = flag.String("credential-id", "", "device credential id (the MQTT username is tenant:credential-id)")
		secretFlag = flag.String("credential-secret", "", "device credential secret (prefer the OTA_CREDENTIAL_SECRET environment variable)")
		origin     = flag.String("origin", "", "instance origin that download grants must point at, e.g. https://dc.example.com")
		downloadCA = flag.String("download-ca", "", "extra CA certificate (PEM) for the download origin (defaults to --ca)")
		component  = flag.String("component", "firmware", "the component this image is")
		product    = flag.String("product", buildinfo.Product, "the product this device is; an image for another product is refused")
		trustFile  = flag.String("trust", "", "PEM file of the public keys this device trusts to sign images")
		allowUnsig = flag.Bool("allow-unsigned", false, "accept an UNVERIFIED assignment (labs only)")
		allowDown  = flag.Bool("allow-downgrade", false, "accept a version that is not newer than the running one (labs only)")
		allowPlain = flag.Bool("allow-insecure-broker", false, "accept a broker URL that is not TLS (labs only)")
		allowHTTP  = flag.Bool("allow-insecure-download", false, "accept a plain-http download base (labs only)")
	)
	flag.Parse()
	lg := log.New(os.Stderr, "ota-device: ", log.LstdFlags)

	for name, v := range map[string]string{"root": *root, "broker": *broker, "instance": *instance, "tenant": *tenant, "device": *deviceTok, "credential-id": *credID, "origin": *origin} {
		if v == "" {
			lg.Printf("--%s is required", name)
			return 2
		}
	}
	if bu, err := url.Parse(*broker); err != nil || bu.Host == "" {
		lg.Printf("--broker must be a URL such as ssl://mqtt.example.com:8883")
		return 2
	} else if !tlsScheme[bu.Scheme] && !*allowPlain {
		lg.Printf("--broker scheme %q is not TLS (ssl, tls, mqtts, tcps, wss); pass --allow-insecure-broker to accept that", bu.Scheme)
		return 2
	}
	secret := *secretFlag
	if secret == "" {
		secret = os.Getenv("OTA_CREDENTIAL_SECRET")
	}
	org, err := url.Parse(*origin)
	if err != nil || org.Host == "" || (org.Scheme != "https" && org.Scheme != "http") {
		lg.Printf("--origin must be an absolute http(s) URL")
		return 2
	}
	if org.Scheme == "http" && !*allowHTTP {
		lg.Printf("--origin is plain http; pass --allow-insecure-download to accept that")
		return 2
	}
	var trust *verify.TrustSet
	if *trustFile != "" {
		if trust, err = verify.LoadTrust(*trustFile); err != nil {
			lg.Printf("%v", err)
			return 2
		}
	}
	if trust == nil && !*allowUnsig {
		lg.Printf("--trust is required unless --allow-unsigned is given")
		return 2
	}
	if err := os.MkdirAll(*root, 0o700); err != nil {
		lg.Print(err)
		return 1
	}

	dlCA := *downloadCA
	if dlCA == "" {
		dlCA = *ca
	}
	httpc, err := device.NewHTTPClient(dlCA, "", org, *allowHTTP)
	if err != nil {
		lg.Print(err)
		return 1
	}
	var rb [4]byte
	_, _ = rand.Read(rb[:])
	bootID := fmt.Sprintf("%d-%s", time.Now().UnixNano(), hex.EncodeToString(rb[:]))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	rebootCh := make(chan struct{})
	var once sync.Once

	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: *serverName}
	if *ca != "" {
		pem, err := os.ReadFile(*ca)
		if err != nil {
			lg.Print(err)
			return 1
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			lg.Printf("no certificate in %s", *ca)
			return 1
		}
		tlsCfg.RootCAs = pool
	}

	prefix := *instance + "/" + *tenant
	reports := prefix + "/device-reports/" + *deviceTok
	desired := prefix + "/device-desired/" + *deviceTok
	inbox := make(chan []byte, 64)

	opts := mqtt.NewClientOptions().AddBroker(*broker).
		SetClientID(*instance + ":" + *tenant + ":" + *deviceTok).
		SetUsername(*tenant + ":" + *credID).SetPassword(secret).
		SetTLSConfig(tlsCfg).SetCleanSession(true).SetAutoReconnect(true).SetConnectRetry(true).
		SetConnectRetryInterval(2 * time.Second).SetMaxReconnectInterval(30 * time.Second)

	sender := &mqttSender{topic: reports}
	agent, err := device.New(device.Config{
		Root: *root, Slot: os.Getenv("OTA_SLOT"), Component: *component, Product: *product,
		Version: buildinfo.Version, BootID: bootID, Trust: trust, RequireSigned: !*allowUnsig,
		AllowDowngrade: *allowDown, Origin: org, AllowInsecure: *allowHTTP, HTTP: httpc, Async: true,
		Reboot: func() { once.Do(func() { close(rebootCh) }) },
		Log:    func(f string, a ...any) { lg.Printf(f, a...) },
	}, sender)
	if err != nil {
		lg.Print(err)
		return 2
	}

	opts.SetOnConnectHandler(func(c mqtt.Client) {
		go func() {
			t := c.Subscribe(desired, 1, func(_ mqtt.Client, m mqtt.Message) {
				b := append([]byte(nil), m.Payload()...)
				select {
				case inbox <- b:
				default:
					lg.Printf("inbox full, dropping a message")
				}
			})
			if !t.WaitTimeout(15*time.Second) || t.Error() != nil {
				lg.Printf("subscribe failed: %v", t.Error())
				return
			}
			// Subscribed before the sync: nothing is retained, so a reply sent before this point is lost.
			if err := agent.OnConnect(ctx); err != nil {
				lg.Printf("on connect: %v", err)
			}
		}()
	})
	opts.SetConnectionLostHandler(func(_ mqtt.Client, err error) { lg.Printf("connection lost: %v", err) })

	client := mqtt.NewClient(opts)
	sender.c = client
	client.Connect() // retries in the background (SetConnectRetry)
	lg.Printf("version %s (product %s) slot %q bootId %s", buildinfo.Version, *product, os.Getenv("OTA_SLOT"), bootID)

	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			agent.Tick(ctx)
		case b := <-inbox:
			agent.Handle(ctx, b)
		case <-rebootCh:
			client.Disconnect(500)
			return boot.ExitReboot
		case <-ctx.Done():
			client.Disconnect(250)
			return 0
		}
	}
}
