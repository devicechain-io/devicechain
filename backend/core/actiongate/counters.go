// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package actiongate

import (
	"time"

	"gorm.io/gorm"
)

// The rate and flood limits. The tenant's policy can only narrow them; the per-tenant tier
// ceiling that would set the first one per tenant is not in this release, so the platform value
// is the ceiling for every tenant.
const (
	// PlatformActionsPerHour bounds the proposals one agent client, and separately one principal,
	// may make in a rolling hour in one area.
	PlatformActionsPerHour = 200
	// DefaultOpenPerClient bounds the proposals one client may have waiting at once, so an
	// injected agent cannot bury a real request in a flood the approvers must wade through.
	DefaultOpenPerClient = 20
	// CounterWindow is the rolling window the hourly counts are taken over.
	CounterWindow = time.Hour
)

// The refusal reasons of the counters.
const (
	ReasonClientRateExceeded    = "CLIENT_RATE_EXCEEDED"
	ReasonPrincipalRateExceeded = "PRINCIPAL_RATE_EXCEEDED"
	ReasonTooManyOpen           = "TOO_MANY_OPEN_PROPOSALS"
)

// Limits are the counters' limits for one proposal.
type Limits struct {
	ActionsPerHour int
	OpenPerClient  int
}

// LimitsFor returns the limits under a decision's actionsPerHour: the policy's value when it is
// positive and below the platform ceiling, the ceiling otherwise. A policy can narrow the ceiling
// and never widen it.
func LimitsFor(actionsPerHour int) Limits {
	l := Limits{ActionsPerHour: PlatformActionsPerHour, OpenPerClient: DefaultOpenPerClient}
	if actionsPerHour > 0 && actionsPerHour < l.ActionsPerHour {
		l.ActionsPerHour = actionsPerHour
	}
	return l
}

// Counts are what the ledger holds for one proposal's client and principal, read under
// LockCounters in the transaction that will insert it.
type Counts struct {
	// ClientHour and PrincipalHour count every proposal of the client, and of the principal, made
	// inside the window, in any state: a refused proposal is still a request the agent made.
	ClientHour    int64
	PrincipalHour int64
	// ClientOpen counts the client's proposals still PROPOSED.
	ClientOpen int64
}

// Refusal returns the reason c, with one more proposal, would exceed l, or "" when it would not.
func (l Limits) Refusal(c Counts) string {
	switch {
	case c.ClientHour >= int64(l.ActionsPerHour):
		return ReasonClientRateExceeded
	case c.PrincipalHour >= int64(l.ActionsPerHour):
		return ReasonPrincipalRateExceeded
	case c.ClientOpen >= int64(l.OpenPerClient):
		return ReasonTooManyOpen
	}
	return ""
}

// LockCounters serializes, for the rest of the transaction tx, every proposal of (tenant,
// client) and then of (tenant, principal), so the counts the limits are checked against are the
// counts the insert lands on: two replicas cannot both take the last slot, and nor can two clients
// of the same person.
//
// 🔴 THE TWO LOCKS ARE TAKEN IN ONE FIXED ORDER, client first. Two transactions that took them in
// opposite orders could each hold one and wait for the other forever.
//
// Anywhere but PostgreSQL (the SQLite unit-test database, which has one writer at a time) there is
// nothing to take. scope separates one area's ledger from another's.
func LockCounters(tx *gorm.DB, scope, tenant, client, principal string) error {
	if tx.Dialector.Name() != "postgres" {
		return nil
	}
	for _, key := range []string{
		"agent-proposals/" + scope + "/client/" + tenant + "/" + client,
		"agent-proposals/" + scope + "/principal/" + tenant + "/" + principal,
	} {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", key).Error; err != nil {
			return err
		}
	}
	return nil
}
