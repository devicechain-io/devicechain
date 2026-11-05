// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package otafleet is the device side of the over-the-air update protocol for a simulated fleet,
// connected to a real broker: each device subscribes to its device-desired topic, receives the
// assignment, asks for a download grant, fetches the image from the capability URL with Range
// requests, verifies size, SHA-256 and signature, reports every stage on device-reports, "applies"
// the update (the version string changes, nothing is executed) and reports the new version.
//
// It adds no update logic. The stages, the persistence, the resumable download and the
// verification are the updater package, which speaks the wire from its own structs and is held to
// the core contract's golden fixtures; this package is the broker wiring and the fault injection
// around it. A device that cannot verify an image reports FAILED with the reason and never RUNNING.
package otafleet

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-simulator/updater"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/rs/zerolog/log"
)

// DefaultComponent is the component the simulated devices manage unless told otherwise.
const DefaultComponent = "firmware"

// Tuning: bounded waits, so an unreachable broker fails a device fast instead of hanging the run.
const (
	connectTimeout   = 15 * time.Second
	subscribeTimeout = 15 * time.Second
	publishTimeout   = 10 * time.Second
	tickEvery        = 500 * time.Millisecond
	grantRetryAfter  = 30 * time.Second
	queueDepth       = 256
)

// otaSessionSuffix makes the update session a second session of the device: the plain client id
// belongs to the command far end, and one client id is one MQTT session. The platform admits a
// device's id followed by ":" and a discriminator of its own.
const otaSessionSuffix = ":ota"

// ErrOffline is what a device's report send returns while it has no broker connection. The
// updater keeps the report pending and sends it again, so nothing is lost by it.
var ErrOffline = errors.New("otafleet: the device is not connected to the broker")

// Options configure a Fleet.
type Options struct {
	// Component is the one component each device manages. Default "firmware".
	Component string
	// InitialVersion is the version a fresh device runs. Required.
	InitialVersion string
	// StateDir holds one directory of device-local state per device (the updater's attempt file),
	// so a restarted simulator continues the same attempts. Empty uses a fresh temporary
	// directory, removed on Close.
	StateDir string
	// TrustedKeys are the PEM public keys (ECDSA P-256 or Ed25519) whose signatures the devices
	// accept: the device-side trust set. A SIGNED assignment is verified against it whatever
	// AllowUnsigned says.
	TrustedKeys []string
	// AllowUnsigned lets the devices install an UNVERIFIED assignment. Off by default, as it is on
	// the reference device: a device that does not say otherwise refuses unsigned images.
	AllowUnsigned bool
	// ChunkSize is the bytes fetched per ranged request. Default 32 KiB. A small chunk is what
	// makes a download visible as progress, and gives an interruption something to resume.
	ChunkSize int64
	// StepDelay pauses a device between steps, to make a rollout watchable. Zero is full speed.
	StepDelay time.Duration
	// Faults picks the misbehaving devices.
	Faults Faults
	// DownloadDialAddr and DownloadInsecure shape the artifact download connection; see
	// DownloadTransport. Both zero is a plain client.
	DownloadDialAddr string
	DownloadInsecure bool
	// Transport, when set, replaces the download transport (tests).
	Transport http.RoundTripper
}

// Validate refuses a configuration that could only produce a fleet of devices that fail every
// update, or no devices at all.
func (o Options) Validate() error {
	switch {
	case o.InitialVersion == "":
		return errors.New("otafleet: InitialVersion is required")
	case o.ChunkSize < 0:
		return errors.New("otafleet: ChunkSize must not be negative")
	case o.StepDelay < 0:
		return errors.New("otafleet: StepDelay must not be negative")
	case len(o.TrustedKeys) == 0 && !o.AllowUnsigned:
		return errors.New("otafleet: the devices trust no signing key and are not allowed to install unsigned " +
			"images, so every update would be refused; give them a trusted public key or allow unsigned images")
	}
	return o.Faults.Validate()
}

// Fleet is the set of simulated updatable devices of one instance and tenant.
type Fleet struct {
	instanceID, tenant, broker string
	tlsConfig                  *tls.Config
	opts                       Options
	stateDir                   string
	ownsStateDir               bool

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu      sync.Mutex
	devices map[string]*device
	order   []string
	closed  bool
}

// New builds a fleet against the MQTT broker (the NATS MQTT gateway). It opens no connection
// until Attach.
func New(instanceID, tenant, broker string, tlsConfig *tls.Config, opts Options) (*Fleet, error) {
	if opts.Component == "" {
		opts.Component = DefaultComponent
	}
	if opts.ChunkSize == 0 {
		opts.ChunkSize = 32 << 10
	}
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	if broker == "" {
		return nil, errors.New("otafleet: no MQTT broker address")
	}
	f := &Fleet{instanceID: instanceID, tenant: tenant, broker: broker, tlsConfig: tlsConfig, opts: opts,
		stateDir: opts.StateDir, devices: map[string]*device{}}
	if f.stateDir == "" {
		dir, err := os.MkdirTemp("", "dc-sim-ota-*")
		if err != nil {
			return nil, err
		}
		f.stateDir, f.ownsStateDir = dir, true
	}
	f.ctx, f.cancel = context.WithCancel(context.Background())
	return f, nil
}

// Attach adds the device at position index of the fleet. An online device returns only once its
// desired-topic subscription is acknowledged, so a device that connects but is never subscribed is
// an error and not a quietly deaf one. An Offline-mode device is registered and left off the
// broker.
func (f *Fleet) Attach(ctx context.Context, deviceToken, credentialID string, index int) error {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return errors.New("otafleet: the fleet is closed")
	}
	if _, dup := f.devices[deviceToken]; dup {
		f.mu.Unlock()
		return fmt.Errorf("otafleet: device %q is already attached", deviceToken)
	}
	f.mu.Unlock()

	clientID, err := messaging.DeviceClientID(f.instanceID, f.tenant, deviceToken)
	if err != nil {
		return fmt.Errorf("otafleet cannot attach device %q: %w", deviceToken, err)
	}
	dir := filepath.Join(f.stateDir, deviceToken)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	d := &device{
		fleet: f, token: deviceToken, credentialID: credentialID, clientID: clientID + otaSessionSuffix,
		mode:         f.opts.Faults.ModeFor(index),
		desiredTopic: fmt.Sprintf("%s/%s/device-desired/%s", f.instanceID, f.tenant, deviceToken),
		reportsTopic: fmt.Sprintf("%s/%s/device-reports/%s", f.instanceID, f.tenant, deviceToken),
		msgs:         make(chan []byte, queueDepth),
		hello:        make(chan struct{}, 1),
	}
	base := f.opts.Transport
	if base == nil {
		base = DownloadTransport(f.opts.DownloadDialAddr, f.opts.DownloadInsecure)
	}
	d.faultRT = &faultTransport{next: base, mode: d.mode}

	cl, err := updater.OpenClient(updater.ClientConfig{
		StatePath: filepath.Join(dir, "updater.json"), Component: f.opts.Component,
		InitialVersion: f.opts.InitialVersion, ChunkSize: f.opts.ChunkSize,
		TrustedKeys: f.opts.TrustedKeys, RequireSignature: updater.RequireSignatureOption(!f.opts.AllowUnsigned),
		Transport: d, HTTP: &http.Client{Timeout: 30 * time.Second, Transport: d.faultRT},
	})
	if err != nil {
		return fmt.Errorf("device %q: %w", deviceToken, err)
	}
	d.client = cl
	d.config, err = updater.OpenConfigAgent(updater.ConfigAgentConfig{Transport: d})
	if err != nil {
		return fmt.Errorf("device %q: %w", deviceToken, err)
	}
	if d.mode == BadSignature {
		cl.Updater().SetFaults(updater.Faults{CorruptSignature: true})
	}

	f.mu.Lock()
	f.devices[deviceToken] = d
	f.order = append(f.order, deviceToken)
	f.mu.Unlock()

	f.wg.Add(1)
	go func() { defer f.wg.Done(); d.run(f.ctx) }()

	if d.mode == Offline {
		d.setError("offline (injected)")
		if after := f.opts.Faults.OfflineFor; after > 0 {
			f.wg.Add(1)
			go func() {
				defer f.wg.Done()
				select {
				case <-time.After(after):
				case <-f.ctx.Done():
					return
				}
				if err := d.connect(f.ctx); err != nil {
					d.setError("coming online: " + err.Error())
					log.Warn().Err(err).Str("device", deviceToken).Msg("otafleet: offline device could not come online")
					return
				}
				d.setError("")
			}()
		}
		return nil
	}
	return d.connect(ctx)
}

// Close disconnects every device and stops their workers. A temporary state directory is removed;
// one the caller named is left, so the next run continues from it.
func (f *Fleet) Close() {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	f.closed = true
	devs := make([]*device, 0, len(f.devices))
	for _, t := range f.order {
		devs = append(devs, f.devices[t])
	}
	f.mu.Unlock()
	f.cancel()
	for _, d := range devs {
		d.disconnect()
	}
	f.wg.Wait()
	if f.ownsStateDir {
		_ = os.RemoveAll(f.stateDir)
	}
}

// device is one simulated updatable device.
type device struct {
	fleet        *Fleet
	token        string
	credentialID string
	clientID     string
	mode         Mode
	desiredTopic string
	reportsTopic string

	client  *updater.Client
	config  *updater.ConfigAgent
	faultRT *faultTransport

	msgs  chan []byte
	hello chan struct{}

	mu        sync.Mutex
	mqtt      mqtt.Client
	connected bool
	lastErr   string
	closed    bool
	dead      bool
	lastStage string
	lastCode  string
	refused   int
	awaiting  time.Time
}

func (d *device) setError(s string) {
	d.mu.Lock()
	d.lastErr = s
	d.mu.Unlock()
}

// connect opens the device's own MQTT session and subscribes it to its desired topic.
func (d *device) connect(ctx context.Context) error {
	f := d.fleet
	opts := mqtt.NewClientOptions()
	opts.AddBroker(f.broker)
	opts.SetClientID(d.clientID)
	opts.SetUsername(fmt.Sprintf("%s:%s", f.tenant, d.credentialID))
	opts.SetPassword("")
	// A persistent session with auto-reconnect: the subscription survives a blip, and OnConnect
	// re-establishes it (and syncs) after a longer loss.
	opts.SetCleanSession(false)
	opts.SetAutoReconnect(true)
	opts.SetConnectTimeout(connectTimeout)
	if f.tlsConfig != nil {
		opts.SetTLSConfig(f.tlsConfig)
	}
	ready := make(chan error, 1)
	var once sync.Once
	opts.OnConnect = func(c mqtt.Client) {
		// Read the SUBACK, do not merely wait for it: a refused subscription is reported by paho
		// as success, and a device that is subscribed to nothing would look like a working one.
		err := messaging.SubscribeMqttConfirmed(c, d.desiredTopic, 1, d.onMessage, subscribeTimeout)
		d.mu.Lock()
		d.connected = err == nil
		d.mu.Unlock()
		if err != nil {
			log.Warn().Err(err).Str("device", d.token).Msg("otafleet: desired subscription failed; the device is deaf until a later reconnect succeeds")
		} else {
			select {
			case d.hello <- struct{}{}:
			default:
			}
		}
		once.Do(func() { ready <- err })
	}
	opts.OnConnectionLost = func(_ mqtt.Client, err error) {
		d.mu.Lock()
		d.connected = false
		d.mu.Unlock()
		log.Warn().Err(err).Str("device", d.token).Msg("otafleet: connection lost; auto-reconnecting")
	}
	c := mqtt.NewClient(opts)
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return errors.New("otafleet: the fleet is closed")
	}
	d.mqtt = c
	d.mu.Unlock()
	// Every failed or cancelled exit tears the client down: paho's auto-reconnect would otherwise
	// keep a session (and its goroutines) alive that nothing owns any more.
	fail := func(err error) error {
		c.Disconnect(0)
		return err
	}
	tok := c.Connect()
	if !tok.WaitTimeout(connectTimeout) {
		return fail(fmt.Errorf("device %q: MQTT connect to %s timed out", d.token, f.broker))
	}
	if err := tok.Error(); err != nil {
		return fail(fmt.Errorf("device %q: MQTT connect to %s failed: %w", d.token, f.broker, err))
	}
	select {
	case err := <-ready:
		if err != nil {
			return fail(fmt.Errorf("device %q: subscribe to %q failed: %w", d.token, d.desiredTopic, err))
		}
		return nil
	case <-time.After(subscribeTimeout):
		return fail(fmt.Errorf("device %q: subscribe to %q not acked within %s", d.token, d.desiredTopic, subscribeTimeout))
	case <-ctx.Done():
		return fail(ctx.Err())
	}
}

func (d *device) disconnect() {
	d.mu.Lock()
	c := d.mqtt
	d.connected = false
	d.closed = true
	d.mu.Unlock()
	if c != nil {
		c.Disconnect(250)
	}
}

// onMessage queues a desired message for the worker, which handles them one at a time and in
// order. A full queue blocks the broker callback rather than dropping an assignment.
func (d *device) onMessage(_ mqtt.Client, m mqtt.Message) {
	payload := append([]byte(nil), m.Payload()...)
	select {
	case d.msgs <- payload:
	case <-d.fleet.ctx.Done():
	}
}

// Send is the updater's Transport: one report (or download request, or sync) on device-reports.
func (d *device) Send(ctx context.Context, payload []byte) error {
	d.mu.Lock()
	c, up := d.mqtt, d.connected
	d.mu.Unlock()
	if c == nil || !up || !c.IsConnectionOpen() {
		return ErrOffline
	}
	tok := c.Publish(d.reportsTopic, 1, false, payload)
	select {
	case <-tok.Done():
	case <-time.After(publishTimeout):
		return fmt.Errorf("otafleet: publish to %q not acknowledged within %s", d.reportsTopic, publishTimeout)
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := tok.Error(); err != nil {
		return err
	}
	d.note(payload)
	return nil
}

// note records the stage (and failure code) of an update report the broker acknowledged, for the
// status view only. It does not judge anything.
func (d *device) note(payload []byte) {
	var r struct {
		Kind   string `json:"kind"`
		Stage  string `json:"stage"`
		Result *struct {
			Code string `json:"code"`
		} `json:"result"`
	}
	if json.Unmarshal(payload, &r) != nil || r.Kind != "update.progress" {
		return
	}
	d.mu.Lock()
	d.lastStage, d.lastCode = r.Stage, ""
	if r.Result != nil {
		d.lastCode = r.Result.Code
	}
	d.mu.Unlock()
}

// run is the device's worker: it handles what the platform sent, says hello after each connect,
// and drives the update forward.
func (d *device) run(ctx context.Context) {
	tick := time.NewTicker(tickEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case m := <-d.msgs:
			d.handle(ctx, m)
		case <-d.hello:
			d.sayHello(ctx)
		case <-tick.C:
		}
		d.drive(ctx)
	}
}

// sayHello is what a device does on every connect: sync (which makes the platform resend an
// update it should still be working on) and report what it runs.
func (d *device) sayHello(ctx context.Context) {
	if err := d.config.Sync(ctx); err != nil {
		d.setError("sync: " + err.Error())
		return
	}
	if err := d.client.Updater().ReportInventory(ctx); err != nil {
		d.setError("inventory: " + err.Error())
		return
	}
	d.setError("")
}

func (d *device) handle(ctx context.Context, payload []byte) {
	err := d.client.HandleDesired(ctx, payload)
	if errors.Is(err, updater.ErrNotUpdateKind) {
		err = d.config.HandleDesired(ctx, payload)
	}
	if err != nil {
		d.mu.Lock()
		d.refused++
		d.lastErr = "desired message refused: " + err.Error()
		d.mu.Unlock()
		log.Warn().Err(err).Str("device", d.token).Msg("otafleet: the device refused a desired message")
	}
}

// drive steps the update until it is waiting, finished or failing to make progress.
func (d *device) drive(ctx context.Context) {
	d.mu.Lock()
	dead := d.dead
	d.mu.Unlock()
	if dead {
		return
	}
	for ctx.Err() == nil {
		done, err := d.client.Drive(ctx, 1)
		switch {
		case err == nil && done:
			return
		case err == nil:
			// Between steps the device listens: a cancel that arrives mid-download must be seen
			// while the update is still recallable, not after it has installed.
			if delay := d.fleet.opts.StepDelay; delay > 0 {
				select {
				case <-time.After(delay):
				case m := <-d.msgs:
					d.handle(ctx, m)
				case <-ctx.Done():
					return
				}
			} else {
				select {
				case m := <-d.msgs:
					d.handle(ctx, m)
				default:
				}
			}
		case errors.Is(err, updater.ErrAwaitingGrant):
			d.mu.Lock()
			if d.awaiting.IsZero() {
				d.awaiting = time.Now()
			} else if time.Since(d.awaiting) > grantRetryAfter {
				// The request or its answer was lost; asking again is the only way out.
				d.awaiting = time.Time{}
				d.client.ForgetRequest()
			}
			d.mu.Unlock()
			return
		case errors.Is(err, updater.ErrCrashed):
			d.mu.Lock()
			d.dead, d.lastErr = true, err.Error()
			d.mu.Unlock()
			return
		default:
			// A dropped link, a cut download: the updater state is intact and the next tick
			// resumes from the bytes it holds. Said once, not once per retry.
			d.mu.Lock()
			changed := d.lastErr != err.Error()
			d.lastErr = err.Error()
			d.mu.Unlock()
			if changed {
				log.Warn().Err(err).Str("device", d.token).Msg("otafleet: update step failed; will retry from the bytes held")
			}
			return
		}
	}
}

// DeviceReport is one device's view of itself.
type DeviceReport struct {
	Token     string `json:"token"`
	Mode      Mode   `json:"mode"`
	Connected bool   `json:"connected"`
	// Version is what the device runs now.
	Version string `json:"version"`
	// Stage is the latest stage the device generated for its attempt ("" before any), and
	// LastReported the stage (and failure code) of the last report the broker acknowledged.
	Stage        string `json:"stage,omitempty"`
	LastReported string `json:"lastReported,omitempty"`
	BytesHave    int64  `json:"bytesHave,omitempty"`
	AttemptID    string `json:"attemptId,omitempty"`
	LastError    string `json:"lastError,omitempty"`
	Refused      int    `json:"refusedMessages,omitempty"`
	Corrupted    int    `json:"corruptedDownloads,omitempty"`
	Interrupted  int    `json:"interruptions,omitempty"`
	Resumes      int    `json:"resumes,omitempty"`
	// ResumedAtByte is where the download picked up after an interruption.
	ResumedAtByte int64 `json:"resumedAtByte,omitempty"`
}

func (d *device) report() DeviceReport {
	snap := d.client.Updater().Snapshot()
	corrupted, interrupted, resumes, off := d.faultRT.counters()
	d.mu.Lock()
	defer d.mu.Unlock()
	last := d.lastStage
	if d.lastCode != "" {
		last += " " + d.lastCode
	}
	return DeviceReport{
		Token: d.token, Mode: d.mode, Connected: d.connected, Version: snap.RunningVersion,
		Stage: snap.Stage, LastReported: last, BytesHave: snap.BytesHave, AttemptID: snap.AttemptID,
		LastError: d.lastErr, Refused: d.refused, Corrupted: corrupted, Interrupted: interrupted,
		Resumes: resumes, ResumedAtByte: off,
	}
}

// Device is one device's report.
func (f *Fleet) Device(token string) (DeviceReport, bool) {
	f.mu.Lock()
	d, ok := f.devices[token]
	f.mu.Unlock()
	if !ok {
		return DeviceReport{}, false
	}
	return d.report(), true
}

// maxListed bounds the per-device failure list in a Report.
const maxListed = 20

// Report is the fleet's /status view: totals, and the devices that need a human's attention.
type Report struct {
	Devices   int `json:"devices"`
	Connected int `json:"connected"`
	// Modes counts devices by injected behaviour, Versions by what they run, Stages by the latest
	// stage they generated ("idle" before any).
	Modes    map[Mode]int   `json:"modes"`
	Versions map[string]int `json:"versions"`
	Stages   map[string]int `json:"stages"`
	// Counts are the fault-injection tallies: bytes corrupted on the wire, downloads cut, and
	// resumes that followed.
	Counts   map[string]int `json:"counts"`
	Failures []DeviceReport `json:"failures,omitempty"`
}

// Report snapshots the fleet.
func (f *Fleet) Report() Report {
	f.mu.Lock()
	devs := make([]*device, 0, len(f.order))
	for _, t := range f.order {
		devs = append(devs, f.devices[t])
	}
	f.mu.Unlock()
	r := Report{Devices: len(devs), Modes: map[Mode]int{}, Versions: map[string]int{}, Stages: map[string]int{},
		Counts: map[string]int{"corruptedDownloads": 0, "interruptions": 0, "resumes": 0, "refusedMessages": 0}}
	for _, d := range devs {
		dr := d.report()
		if dr.Connected {
			r.Connected++
		}
		r.Modes[dr.Mode]++
		r.Versions[dr.Version]++
		stage := dr.Stage
		if stage == "" {
			stage = "idle"
		}
		r.Stages[stage]++
		r.Counts["corruptedDownloads"] += dr.Corrupted
		r.Counts["interruptions"] += dr.Interrupted
		r.Counts["resumes"] += dr.Resumes
		r.Counts["refusedMessages"] += dr.Refused
		if (dr.Stage == "FAILED" || dr.Refused > 0) && len(r.Failures) < maxListed {
			r.Failures = append(r.Failures, dr)
		}
	}
	sort.Slice(r.Failures, func(i, j int) bool { return r.Failures[i].Token < r.Failures[j].Token })
	return r
}
