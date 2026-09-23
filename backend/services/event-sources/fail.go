// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import "github.com/rs/zerolog/log"

// failProcess ends the process with a non-zero status, for a cause found after startup.
// It is this service's ONE way of doing that; nothing here calls log.Fatal once the
// service is running.
//
// 🔴 FailNow, NOT log.Fatal. log.Fatal is an immediate os.Exit: it skips the readiness
// drain (the pod keeps receiving traffic until the moment it dies), beforeMicroserviceStopped
// (every event source's Stop, the GraphQL server, and the NATS manager's drain of
// publishes still in flight) and the terminator. FailNow runs all of that and still
// exits non-zero, which is what tells a pod that failed apart from one that was rolled.
//
// 🔴 OFF THE CALLER'S GOROUTINE, because the callers can be what the teardown waits for.
// beforeMicroserviceStopped runs stopBrokerPresence, which waits on rt.stopped — and the
// presence demote loop, which is the goroutine that closes rt.stopped, is one of the
// callers (restartForRecoveredBroker). Inline, that goroutine would park inside FailNow
// and the wait would run out its five-second cap on every such exit. The other caller is
// an external MQTT source's OnConnect, on one of paho's goroutines. Nothing waits on that
// goroutine, but a paho callback is still no place to run the whole teardown, which
// stops that same client.
//
// The `go` is here and not in endProcess so that a test can replace endProcess without
// deleting the thing under test.
func failProcess(err error) {
	go endProcess(err)
}

// endProcess is failProcess's synchronous half. It sits behind a variable so a test can
// observe the call without ending the test binary; only a test ever replaces it.
var endProcess = func(err error) {
	if Microservice == nil {
		log.Error().Err(err).Msg("event-sources cannot end the process; it has no microservice handle.")
		return
	}
	Microservice.FailNow(err)
}
