// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/devicechain-io/dc-simulator/sim"
	"github.com/devicechain-io/dc-simulator/x509sim"
)

// x509Flags are the --x509 mode's settings.
type x509Flags struct {
	dir, prefix, anchor, deviceType, serverCA *string
	pinned, jit                               *int
	register, mintOnly                        *bool
	validity, holdFor                         *time.Duration
}

// runX509 is the X.509 device mode: mint, optionally register, connect, hold, summarise.
//
// A refusal at connect is a RESULT, not a failure: a JIT certificate is expected to be refused on
// a build with no registration path, and a rig asserts on the summary line (stdout, one JSON
// object) rather than on this process's exit status, which says only whether the run could be made.
func runX509(handshakePath string, f x509Flags) error {
	hs, err := sim.LoadHandshake(handshakePath)
	if err != nil {
		return err
	}
	st, err := x509sim.OpenOrMint(*f.dir, x509sim.Options{
		Prefix: *f.prefix, AnchorToken: *f.anchor, Pinned: *f.pinned, JIT: *f.jit, DeviceValidity: *f.validity,
	})
	if err != nil {
		return err
	}
	log.Info().Str("dir", st.Dir).Int("pinned", len(st.Entries(x509sim.KindPinned))).
		Int("jit", len(st.Entries(x509sim.KindJIT))).Msg("x509 state ready")

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if *f.register {
		rt, err := sim.NewRuntime(hs, sim.Load{}, 1)
		if err != nil {
			return err
		}
		if err := x509sim.Register(ctx, rt.Session, x509sim.RegisterConfig{
			Endpoint: hs.Endpoints.DeviceMgmtGraphQL, DeviceTypeToken: *f.deviceType,
		}, st); err != nil {
			return err
		}
	}
	if *f.mintOnly {
		return nil
	}
	if hs.InstanceId == "" || hs.Endpoints.MqttBroker == "" {
		return fmt.Errorf("the handshake carries no instanceId or mqttBroker to connect to")
	}
	fleet, err := x509sim.Connect(ctx, x509sim.ConnectConfig{
		Broker: hs.Endpoints.MqttBroker, InstanceId: hs.InstanceId, Tenant: hs.Tenant,
		ServerCAFile: *f.serverCA, ServerInsecure: hs.MqttTLSInsecure,
	}, st, st.Manifest.Devices)
	if fleet != nil {
		defer fleet.Close()
	}
	if err != nil {
		return err
	}
	pc, pr := fleet.Summary(x509sim.KindPinned)
	jc, jr := fleet.Summary(x509sim.KindJIT)
	out, err := json.Marshal(map[string]any{
		"pinned":  map[string]int{"connected": pc, "refused": pr},
		"jit":     map[string]int{"connected": jc, "refused": jr},
		"results": fleet.Results,
	})
	if err != nil {
		return err
	}
	fmt.Println(string(out))

	if *f.holdFor > 0 {
		select {
		case <-ctx.Done():
		case <-time.After(*f.holdFor):
		}
		return nil
	}
	<-ctx.Done()
	return nil
}
