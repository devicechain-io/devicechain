// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

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
// It is safe to call from the goroutines that call it, which can be what the teardown
// waits for, because Microservice.FailNow returns at once and runs the teardown on its own
// goroutine (core pins that contract). The caller that depends on it is the presence
// demote loop (restartForRecoveredBroker): beforeMicroserviceStopped runs
// stopBrokerPresence, which waits on rt.stopped, and the demote loop is the goroutine
// that closes it. The other caller is an external MQTT source's OnConnect, on one of
// paho's goroutines.
func failProcess(err error) {
	endProcess(err)
}

// endProcess sits behind a variable so a test can observe the call without ending the
// test binary; only a test ever replaces it. A nil Microservice is FailNow's to report.
var endProcess = func(err error) {
	Microservice.FailNow(err)
}
