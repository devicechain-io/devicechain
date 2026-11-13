// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"

	nats "github.com/nats-io/nats.go"

	"github.com/devicechain-io/dc-microservice/messaging"
)

// GatewayAnswers tells a gateway which of its events the platform durably accepted, or shed at
// the tenant's ceiling (see processor.GatewayFeedback). Built in createNatsComponents, ahead of
// every reader, and stopped first in beforeMicroserviceStopped so its last batch goes out on a
// connection that is still open.
var (
	GatewayAnswers      *messaging.GatewayFeedback
	gatewayAnswersStop  context.CancelFunc
	gatewayAnswersAlive chan struct{}
)

func startGatewayFeedback(nmgr *messaging.NatsManager) {
	GatewayAnswers = messaging.NewGatewayFeedback(messaging.NewGatewayNoticePublisher(
		func() *nats.Conn { return nmgr.Conn() }, Microservice.InstanceId))
	ctx, cancel := context.WithCancel(context.Background())
	gatewayAnswersStop = cancel
	gatewayAnswersAlive = make(chan struct{})
	go func() {
		defer close(gatewayAnswersAlive)
		GatewayAnswers.Run(ctx)
	}()
}

func stopGatewayFeedback() {
	if gatewayAnswersStop == nil {
		return
	}
	gatewayAnswersStop()
	<-gatewayAnswersAlive
	gatewayAnswersStop = nil
}
