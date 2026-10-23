// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Command calloutfixture runs a REAL nats-server MQTT gateway behind the platform's auth callout,
// with a small responder standing in for device-management, so a device SDK can be driven through
// the admission path a deployed instance imposes instead of through an authless broker.
//
// An authless broker says yes to every CONNECT, every SUBSCRIBE and every PUBLISH, so a test run
// against one cannot see the failures that matter most to a device: a username the callout will
// not parse, a client id it refuses, a credential it does not know, or a per-device grant that does
// not cover what the SDK asks for. This fixture closes that gap with the platform's own pieces:
//
//   - the broker is configured the way the deployed one is -- an APP account holding the static
//     service login, an auth_callout delegating everything else, and the callout's xkey so the
//     CONNECT credentials travel encrypted to the responder;
//   - the responder mints each admitted device's grant with natsauth.SignDeviceUserJWT, the same
//     function device-management calls, and enforces the same client-id rule
//     (messaging.DeviceClientID / DeviceClientIDMatches);
//   - a credential is the access-token mode of the device plane: the CONNECT username is
//     "{tenant}:{credential}" with an empty password, and the credential names exactly one seeded
//     device.
//
// What it is NOT is device-management: there is no database, no credential store, no revocation and
// no resolver. The part of admission that lives in those (does this credential still exist, is it
// revoked, is the event attributable) is covered where a database is available; what is covered
// here is the part a device can get wrong on the wire.
//
// Besides the broker the fixture runs a tiny HTTP control surface on loopback, so a test can do the
// two things a device cannot do for itself and read back what the platform side saw:
//
//	POST /command?device=D&token=T&name=N   publish a command to D over NATS, as command-delivery does
//	GET  /events?device=D                   the bodies the broker delivered on D's events subject
//	GET  /responses?device=D                the bodies the broker delivered on D's responses subject
//
// Reading back from the NATS side matters: a PUBACK is the broker taking ownership of an MQTT
// publish, and says nothing about the grant having let it through to a subscriber.
//
// Usage, from backend/core:
//
//	go run ./natsauth/cmd/calloutfixture -out fixture.json
//
// The file named by -out is written only once the broker and the responder are ready, so waiting
// for it to appear is the readiness check; it holds the MQTT URL, the control URL and the
// credential of each seeded device. The process then runs until it is interrupted.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/natsauth"
	"github.com/nats-io/jwt/v2"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

// fixtureOut is the file the fixture writes once it is ready. The C# tests parse it; its field
// names are a contract with RealCalloutFixture in
// sdks/csharp/tests/DeviceChain.Sdk.Tests/MqttRealCalloutTests.cs.
type fixtureOut struct {
	MqttURL    string          `json:"mqttUrl"`
	ControlURL string          `json:"controlUrl"`
	Instance   string          `json:"instance"`
	Tenant     string          `json:"tenant"`
	Devices    []fixtureDevice `json:"devices"`
}

type fixtureDevice struct {
	Token      string `json:"token"`
	Credential string `json:"credential"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "calloutfixture:", err)
		os.Exit(1)
	}
}

func run() error {
	out := flag.String("out", "", "file to write the fixture description to once ready (required)")
	instance := flag.String("instance", "inst", "instance id")
	tenant := flag.String("tenant", "acme", "tenant token")
	devices := flag.String("devices", "g10-sensor,g10-other", "comma-separated device tokens to seed, each with its own credential")
	flag.Parse()
	if *out == "" {
		return errors.New("-out is required")
	}

	creds, err := natsauth.GenerateCredentials()
	if err != nil {
		return err
	}

	// Credential -> device. The credential is the access token of the device plane: random, held
	// only here, handed to the test through -out.
	byCredential := map[string]string{}
	var seeded []fixtureDevice
	for _, tok := range strings.Split(*devices, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		raw := make([]byte, 16)
		if _, err := rand.Read(raw); err != nil {
			return err
		}
		cred := hex.EncodeToString(raw)
		byCredential[cred] = tok
		seeded = append(seeded, fixtureDevice{Token: tok, Credential: cred})
	}
	if len(seeded) == 0 {
		return errors.New("-devices names no device")
	}

	dir, err := os.MkdirTemp("", "calloutfixture-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	srv, err := startBroker(dir, creds)
	if err != nil {
		return err
	}
	defer srv.Shutdown()

	svc, err := nats.Connect(srv.ClientURL(), nats.UserInfo(natsauth.ServiceUser, creds.ServicePassword))
	if err != nil {
		return fmt.Errorf("service connect: %w", err)
	}
	defer svc.Close()

	codec, err := natsauth.NewXKeyCodec(creds.XKeySeed)
	if err != nil {
		return err
	}
	if err := startResponder(svc, codec, creds.IssuerSeed, *instance, *tenant, byCredential); err != nil {
		return err
	}

	rec := &recorder{bodies: map[string][]string{}}
	if err := recordSubjects(svc, rec, *instance, *tenant); err != nil {
		return err
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	httpSrv := &http.Server{Handler: controlHandler(svc, rec, *instance, *tenant), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = httpSrv.Serve(ln) }()
	defer httpSrv.Shutdown(context.Background())

	vz, err := srv.Varz(nil)
	if err != nil {
		return err
	}
	if vz.MQTT.Port <= 0 {
		return fmt.Errorf("the broker reports no bound MQTT port (%d)", vz.MQTT.Port)
	}
	desc := fixtureOut{
		MqttURL:    fmt.Sprintf("tcp://127.0.0.1:%d", vz.MQTT.Port),
		ControlURL: "http://" + ln.Addr().String(),
		Instance:   *instance,
		Tenant:     *tenant,
		Devices:    seeded,
	}
	body, err := json.MarshalIndent(desc, "", "  ")
	if err != nil {
		return err
	}
	// Written to a sibling and renamed, so a reader polling for the file never sees half of it.
	tmp := *out + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, *out); err != nil {
		return err
	}
	log.Printf("calloutfixture ready: mqtt=%s control=%s", desc.MqttURL, desc.ControlURL)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	return nil
}

// startBroker runs nats-server from a config FILE, because the shape under test is the deployed
// one and Options assembled in Go can express an arrangement the config parser would never
// produce. The listeners are loopback-only.
func startBroker(dir string, creds natsauth.Credentials) (*natsserver.Server, error) {
	conf := filepath.Join(dir, "nats.conf")
	body := fmt.Sprintf(`
		port: -1
		host: "127.0.0.1"
		server_name: "callout-fixture"
		jetstream { store_dir: %q }
		mqtt { host: "127.0.0.1", port: -1 }
		system_account: SYS
		accounts {
		  APP { jetstream: enabled, users: [{user: %q, password: %q}] }
		  SYS {}
		}
		authorization {
		  timeout: 5
		  auth_callout { issuer: %q, xkey: %q, auth_users: [%q], account: "APP" }
		}
	`, filepath.Join(dir, "js"), natsauth.ServiceUser, creds.ServicePassword,
		creds.IssuerPublic, creds.XKeyPublic, natsauth.ServiceUser)
	if err := os.WriteFile(conf, []byte(body), 0o600); err != nil {
		return nil, err
	}
	opts, err := natsserver.ProcessConfigFile(conf)
	if err != nil {
		return nil, fmt.Errorf("process nats config: %w", err)
	}
	opts.NoLog, opts.NoSigs = true, true
	srv, err := natsserver.NewServer(opts)
	if err != nil {
		return nil, err
	}
	go srv.Start()
	if !srv.ReadyForConnections(30 * time.Second) {
		srv.Shutdown()
		return nil, errors.New("nats-server not ready")
	}
	return srv, nil
}

// startResponder answers the broker's authorization requests the way device-management does for a
// device: parse "{tenant}:{credential}", resolve the credential to its device, require the MQTT
// client id to be that device's own, and mint the platform's own grant. Every refusal carries the
// same generic message, so a refusal reveals nothing about which check failed.
func startResponder(svc *nats.Conn, codec *natsauth.XKeyCodec, issuerSeed, instance, tenant string, byCredential map[string]string) error {
	_, err := svc.Subscribe(natsauth.AuthCalloutRequestSubject, func(msg *nats.Msg) {
		data, sealTo := msg.Data, msg.Header.Get(natsauth.AuthRequestXKeyHeader)
		if sealTo != "" {
			plain, err := codec.Open(msg.Data, sealTo)
			if err != nil {
				return
			}
			data = plain
		}
		rc, err := jwt.DecodeAuthorizationRequestClaims(string(data))
		if err != nil {
			return
		}
		req := rc.AuthorizationRequest

		var userJWT, errMsg string
		device, ok := admit(req.ConnectOptions.Username, req.ClientInformation.MQTT, instance, tenant, byCredential)
		if ok {
			userJWT, err = natsauth.SignDeviceUserJWT(issuerSeed, req.UserNkey, instance, tenant, device, time.Now(), time.Hour)
			if err != nil {
				errMsg = "denied"
			}
		} else {
			errMsg = "denied"
		}

		resp, err := natsauth.EncodeAuthResponse(issuerSeed, req.Server.ID, req.UserNkey, userJWT, errMsg)
		if err != nil {
			return
		}
		out := []byte(resp)
		if sealTo != "" {
			if out, err = codec.Seal(out, sealTo); err != nil {
				return
			}
		}
		_ = msg.Respond(out)
	})
	if err != nil {
		return err
	}
	return svc.Flush()
}

// admit is the fixture's whole admission decision, kept apart from the wire so it reads as one.
func admit(username, clientID, instance, tenant string, byCredential map[string]string) (string, bool) {
	tn, cred, found := strings.Cut(username, ":")
	if !found || tn != tenant || cred == "" {
		return "", false
	}
	device, known := byCredential[cred]
	if !known {
		return "", false
	}
	required, err := messaging.DeviceClientID(instance, tn, device)
	if err != nil || !messaging.DeviceClientIDMatches(clientID, required) {
		return "", false
	}
	return device, true
}

// recorder keeps what the broker delivered on the NATS side, keyed by subject.
type recorder struct {
	mu     sync.Mutex
	bodies map[string][]string
}

func (r *recorder) add(subject string, data []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bodies[subject] = append(r.bodies[subject], string(data))
}

func (r *recorder) get(subject string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string{}, r.bodies[subject]...)
}

func recordSubjects(svc *nats.Conn, rec *recorder, instance, tenant string) error {
	for _, subject := range []string{
		messaging.DeviceEventsSubject(instance, tenant, "*"),
		messaging.DeviceScopedSubject(instance, tenant, messaging.SubjectCommandResponses, "*"),
	} {
		if _, err := svc.Subscribe(subject, func(m *nats.Msg) { rec.add(m.Subject, m.Data) }); err != nil {
			return err
		}
	}
	return svc.Flush()
}

func controlHandler(svc *nats.Conn, rec *recorder, instance, tenant string) http.Handler {
	mux := http.NewServeMux()
	device := func(w http.ResponseWriter, r *http.Request) (string, bool) {
		d := r.URL.Query().Get("device")
		if d == "" || strings.ContainsAny(d, ".*> ") {
			http.Error(w, "device is required and must be one token", http.StatusBadRequest)
			return "", false
		}
		return d, true
	}
	mux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		if d, ok := device(w, r); ok {
			_ = json.NewEncoder(w).Encode(rec.get(messaging.DeviceEventsSubject(instance, tenant, d)))
		}
	})
	mux.HandleFunc("/responses", func(w http.ResponseWriter, r *http.Request) {
		if d, ok := device(w, r); ok {
			_ = json.NewEncoder(w).Encode(rec.get(messaging.DeviceScopedSubject(instance, tenant, messaging.SubjectCommandResponses, d)))
		}
	})
	mux.HandleFunc("/command", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		d, ok := device(w, r)
		if !ok {
			return
		}
		token, name := r.URL.Query().Get("token"), r.URL.Query().Get("name")
		if token == "" || name == "" {
			http.Error(w, "token and name are required", http.StatusBadRequest)
			return
		}
		body, _ := json.Marshal(map[string]string{
			"token": token, "deviceToken": d, "name": name, "dispatchNonce": token + "-dispatch",
		})
		if err := svc.Publish(messaging.DeviceScopedSubject(instance, tenant, messaging.SubjectDeviceCommands, d), body); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		if err := svc.Flush(); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	})
	return mux
}
