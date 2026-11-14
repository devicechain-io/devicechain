// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/natsauth"
	server "github.com/nats-io/nats-server/v2/server"
)

func TestBackupBrokerUsesResolvedCredentials(t *testing.T) {
	for _, areaLogins := range []bool{false, true} {
		cfg := config.NatsConfiguration{}
		cfg.Auth.User, cfg.Auth.Password = "legacy", "legacy-password"
		cfg.Auth.AreaLogins = areaLogins
		user, password := cfg.Auth.User, cfg.Auth.Password
		if areaLogins {
			user, password = natsauth.LoginUser(config.NatsAdminPrincipal), "admin-password"
			cfg.Auth.AreaPasswords = map[string]string{config.NatsAdminPrincipal: password}
		}
		srv, err := server.NewServer(&server.Options{Host: "127.0.0.1", Port: -1, NoLog: true, NoSigs: true, Users: []*server.User{{Username: user, Password: password}}})
		if err != nil {
			t.Fatal(err)
		}
		go srv.Start()
		if !srv.ReadyForConnections(5 * time.Second) {
			t.Fatal("broker did not start")
		}
		t.Cleanup(func() { srv.Shutdown(); srv.WaitForShutdown() })
		nc, err := backupBroker(context.Background(), srv.ClientURL(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		if areaLogins && !strings.HasPrefix(nc.NewInbox(), messaging.InboxPrefix(config.NatsAdminPrincipal)+".") {
			t.Fatal("backup uses another principal's reply inbox")
		}
		nc.Close()
	}
}

func TestBackupBrokerDoesNotFallBackFromMissingAdminLogin(t *testing.T) {
	cfg := config.NatsConfiguration{}
	cfg.Auth.AreaLogins = true
	cfg.Auth.User, cfg.Auth.Password = "shared", "shared-password"
	_, err := backupBroker(context.Background(), "nats://127.0.0.1:1", cfg)
	if err == nil || !strings.Contains(err.Error(), "administrator login is missing") {
		t.Fatalf("missing admin login was not rejected before connecting: %v", err)
	}
}
