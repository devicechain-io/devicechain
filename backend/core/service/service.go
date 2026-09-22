// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package service assembles the three managers almost every DeviceChain service holds —
// the relational database, the broker and the GraphQL server — and drives their lifecycle
// in one order that lives here instead of in fourteen copies.
//
// # Why this is not in core/core
//
// rdb, messaging and graphql all import core/core, so core/core cannot import them. This
// package sits above all four, which is the only place a type holding the three can live.
//
// # The order, and the evidence for it
//
// There is ONE sequence — Rdb, then NATS, then GraphQL — walked forwards to bring a
// service up and backwards to take it down:
//
//	Initialize  Rdb -> NATS -> GraphQL
//	Start       Rdb -> NATS -> GraphQL
//	Stop        GraphQL -> NATS -> Rdb
//	Terminate   GraphQL -> NATS -> Rdb
//
// Three of those four were already unanimous across the tree before this package existed:
// every service initialized in that order, every service stopped in its reverse, and the
// start order was made unanimous by the change that put NATS ahead of the GraphQL server.
//
// 🔑 THE START ORDER IS THE ONE WITH A CONSEQUENCE, and it is worth stating here because
// this is now the only place it is written. NatsManager.Start runs the oncreate callback,
// which is where a service builds the wiring its RESOLVERS read — device-management binds
// six publishers into its Api there, command-delivery binds its dispatch nudger. The
// GraphQL server must not be accepting traffic before that has run, and "before" is not
// hypothetical: /readyz is served by the GraphQL manager's own HTTP server, so the moment
// it starts is the moment traffic can first arrive.
//
// Terminate was the one phase services disagreed about — seven ran NATS before GraphQL.
// Unifying it moved nothing, because GraphQLManager.ExecuteTerminate returns nil and no
// service wraps it in a callback: the only thing that changed position was a no-op, and
// NATS still terminates before Rdb, which is the pair that closes real handles.
//
// # What this package does NOT own
//
// Manager CONSTRUCTION is service-specific — migrations, datastore config, the oncreate
// callback, the parsed schema, the context providers — so a Spec supplies those. And the
// readiness gate is left to the caller: most services open it with StartInstanceAuthGate,
// user-management has its own validator and calls MarkReady, and the ingest services open
// it with no auth surface at all. Three different answers is not a default.
//
// Nor is this for every service, and the two kinds of exception are worth telling apart.
//
// The ingest services assemble no GraphQL manager and hold their broker differently —
// lwm2m-ingest RELEASES a leadership lease over its connection during shutdown, so its
// NATS stop must come LAST, which is the opposite of the order here. They keep their own
// wiring, and that is a decision rather than an omission: a sequence that had to carry an
// exception for them would stop being one sequence.
//
// 🔴 user-management IS THE OTHER KIND, AND IT IS THE ONE TO READ BEFORE EXTENDING THIS
// API. It assembles exactly these three managers in exactly this order, but it stops its
// own components BETWEEN two of them: GraphQL, then the purge coordinator and the
// dead-letter pair, then NATS, then Rdb. The coordinator's pass holds an advisory lock on
// a pooled connection, so it has to be down before the database and the broker — and the
// GraphQL server has to be down before it, because that is where a tenant deletion is
// accepted. Every other adopter's components stop wholly before or wholly after the three,
// which is why a single Stop call fits them and not this one.
//
// It was left on its own wiring rather than converted, and the alternative is written down
// here because it will be proposed again: hooks in the downward gaps, symmetric with
// AfterRdb and AfterNats. That is a real design, but today it would have one caller for one
// of the two hooks and none for the other — and the way to earn it is a second service that
// wants the same gap, not a first one that can be made to fit.
package service

import (
	"context"
	"fmt"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"

	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
)

// RdbSpec is what a service supplies to get a relational database manager.
type RdbSpec struct {
	// Migrations is the service's own migration chain.
	Migrations []*gormigrate.Migration

	// Instance is the instance-level datastore this manager opens.
	//
	// 🔴 IT IS NAMED BY THE SERVICE BECAUSE SERVICES DO NOT AGREE, and an earlier version
	// of this struct read InstanceConfiguration.Persistence.Rdb here on the grounds that
	// they did. They do not: event-management's manager opens Persistence.TSDB — the
	// instance's event store, a different cluster — and everything it holds is a
	// hypertable there. Defaulting would have pointed it at the relational store and
	// created its schema in the wrong database.
	//
	// The distinction is load-bearing outside this package too. dcctl sizes an instance's
	// Postgres connection limit from the areas that open a pool on the RELATIONAL store,
	// and recognizes one by the literal it names here — so a service that says which store
	// it opens is counted correctly, and event-management is correctly not counted.
	Instance mscfg.DatastoreConfiguration

	// Config is the service's own half of that datastore's configuration.
	Config mscfg.MicroserviceDatastoreConfiguration
}

// NatsSpec is what a service supplies to get a broker manager.
type NatsSpec struct {
	// OnCreate builds the service's readers, writers and the components that hold them.
	//
	// 🔴 IT RUNS IN NatsManager.START, NOT IN ITS INITIALIZE, and that is the whole
	// reason the start order above matters. Anything this binds does not exist until the
	// broker is up, so a resolver that reads it must not be reachable before then.
	OnCreate func(*messaging.NatsManager) error
}

// GraphQLSpec is what a service supplies to get a GraphQL server.
type GraphQLSpec struct {
	// Schema is the SDL. It is a constant, so it is a value.
	Schema string

	// Resolver returns the root resolver, and Providers the request-context providers.
	//
	// 🔑 BOTH ARE FUNCTIONS FOR THE SAME REASON: they are evaluated when the GraphQL
	// manager is BUILT, which is after AfterRdb has run, and what they return generally
	// depends on what AfterRdb made. Providers carries the Api; event-processing's
	// resolver carries six read-model stores, all of them wrapped around the relational
	// manager.
	//
	// 🔴 A PLAIN VALUE HERE WOULD BE CAPTURED WHEN THE SPEC LITERAL IS WRITTEN, which is
	// before any of that exists. That is the trap this signature exists to close: the
	// field is read late, so a value written into it reads as though it were computed
	// late, and a resolver built that way carries nil stores into a server that compiles,
	// starts and serves — failing at the first query rather than at startup.
	Resolver  func() interface{}
	Providers func() map[gqlcore.ContextKey]interface{}
}

// Spec describes the managers a service wants and the one place it needs to do its own
// wiring in the middle of building them.
type Spec struct {
	// Rdb, Nats and GraphQL are each optional: a nil one means the service does not have
	// that manager, and nothing is built, driven or reported for it.
	Rdb     *RdbSpec
	Nats    *NatsSpec
	GraphQL *GraphQLSpec

	// AfterRdb runs once the relational manager is initialized, before the broker manager
	// is built. AfterNats runs once the broker manager is initialized, before the GraphQL
	// manager is built. Either may be nil.
	//
	// 🔑 THERE ARE TWO HOOKS BECAUSE THREE CONSTRUCTIONS HAVE TWO GAPS, and both gaps are
	// occupied by real services. This is the whole surface, not an escape hatch that will
	// grow: a service has nothing to do before the first construction (it would just do it
	// before calling New) or after the last (it does it after Initialize returns).
	//
	// Most services need the first gap: the Api wraps the relational manager, and the
	// GraphQL providers carry that Api. Two services need the second, because what they
	// build is backed by the BROKER rather than the database — device-management's entity
	// caches are NATS JetStream KV buckets (ADR-007), and user-management's identity
	// manager needs a KV store for refresh tokens plus a distributed lock to serialize
	// signing-key work across replicas. Neither can exist before the broker manager does.
	//
	// 🔴 NEITHER IS THE PLACE FOR ANYTHING BOUND TO THE CONNECTION. Both run during
	// INITIALIZE, and the broker connection is made in NatsManager's START — so a reader or
	// writer built in AfterNats captures a nil by value and panics on first use. That
	// belongs in NatsSpec.OnCreate. What AfterNats is for is the things a manager can hand
	// out at initialize: KV buckets, locks, and whatever is built from them.
	//
	// Both are handed what has been built so far rather than reading package variables,
	// because at this moment the caller has not been given the managers yet.
	AfterRdb  func(context.Context, *Managers) error
	AfterNats func(context.Context, *Managers) error
}

// Managers holds what a Spec produced. A field is nil when its Spec was.
type Managers struct {
	Rdb     *rdb.RdbManager
	Nats    *messaging.NatsManager
	GraphQL *gqlcore.GraphQLManager
}

// Service is a microservice plus the managers it assembles.
type Service struct {
	*core.Microservice
	Managers

	spec Spec
}

// New records what to build. Nothing is constructed until Initialize.
//
// Construction is deferred because two of the three managers need values that do not
// exist when a service can first name its Spec: the GraphQL providers carry an Api built
// from the Rdb manager, and AfterRdb is what builds it.
func New(ms *core.Microservice, spec Spec) *Service {
	return &Service{Microservice: ms, spec: spec}
}

// FromManagers wraps managers somebody else built, so they can be driven through the same
// one sequence without this package having constructed them.
//
// 🔑 IT EXISTS FOR THE CASE THAT CANNOT GO THROUGH A SPEC: managers deliberately left in
// DIFFERENT lifecycle states, or carrying callbacks of the caller's own. A Spec builds all
// three the same way with no-op callbacks, which is right for a service and useless to a
// test that has to observe the order of two stops by hanging probes on them — and a test
// that drove a private copy of this ordering instead would no longer be testing it.
//
// A Service built this way has nothing to construct, so Initialize on it does nothing and
// succeeds. Start, Stop and Terminate behave exactly as they do for a Spec-built one.
func FromManagers(ms *core.Microservice, m Managers) *Service {
	return &Service{Microservice: ms, Managers: m}
}

// Initialize builds each requested manager and initializes it, in the forward order, with
// AfterRdb run in between.
//
// On a Service built by FromManagers there is no Spec, so every branch below is skipped
// and this returns nil: the managers were initialized by whoever built them.
func (s *Service) Initialize(ctx context.Context) error {
	if s.spec.Rdb != nil {
		s.Rdb = rdb.NewRdbManager(s.Microservice, core.NewNoOpLifecycleCallbacks(),
			s.spec.Rdb.Migrations, s.spec.Rdb.Instance, s.spec.Rdb.Config)
		if err := s.Rdb.Initialize(ctx); err != nil {
			return fmt.Errorf("initializing the relational database manager: %w", err)
		}
	}

	if s.spec.AfterRdb != nil {
		if err := s.spec.AfterRdb(ctx, &s.Managers); err != nil {
			return err
		}
	}

	if s.spec.Nats != nil {
		s.Nats = messaging.NewNatsManager(s.Microservice, core.NewNoOpLifecycleCallbacks(),
			s.spec.Nats.OnCreate)
		if err := s.Nats.Initialize(ctx); err != nil {
			return fmt.Errorf("initializing the broker manager: %w", err)
		}
	}

	if s.spec.AfterNats != nil {
		if err := s.spec.AfterNats(ctx, &s.Managers); err != nil {
			return err
		}
	}

	if s.spec.GraphQL != nil {
		parsed := gqlcore.MustParseSchema(s.spec.GraphQL.Schema, s.spec.GraphQL.Resolver())
		providers := map[gqlcore.ContextKey]interface{}{}
		if s.spec.GraphQL.Providers != nil {
			providers = s.spec.GraphQL.Providers()
		}
		s.GraphQL = gqlcore.NewGraphQLManager(s.Microservice, core.NewNoOpLifecycleCallbacks(),
			parsed, providers, s.Microservice.Readiness)
		if err := s.GraphQL.Initialize(ctx); err != nil {
			return fmt.Errorf("initializing the GraphQL manager: %w", err)
		}
	}
	return nil
}

// Start starts the managers in the forward order.
func (s *Service) Start(ctx context.Context) error {
	return s.forward(ctx, "starting", func(c core.LifecycleComponent) func(context.Context) error {
		return c.Start
	})
}

// Stop stops the managers in the reverse order.
func (s *Service) Stop(ctx context.Context) error {
	return s.reverse(ctx, "stopping", func(c core.LifecycleComponent) func(context.Context) error {
		return c.Stop
	})
}

// Terminate terminates the managers in the reverse order.
func (s *Service) Terminate(ctx context.Context) error {
	return s.reverse(ctx, "terminating", func(c core.LifecycleComponent) func(context.Context) error {
		return c.Terminate
	})
}

// ordered is the ONE sequence, forwards. Everything else reads it or reads it backwards,
// so there is no second list to keep in step with this one.
//
// A nil entry is a manager the Spec did not ask for and is skipped; it is not an error,
// because a service with no broker is an ordinary shape (two of them serve GraphQL over a
// database alone).
func (s *Service) ordered() []core.LifecycleComponent {
	out := make([]core.LifecycleComponent, 0, 3)
	if s.Rdb != nil {
		out = append(out, s.Rdb)
	}
	if s.Nats != nil {
		out = append(out, s.Nats)
	}
	if s.GraphQL != nil {
		out = append(out, s.GraphQL)
	}
	return out
}

func (s *Service) forward(ctx context.Context, verb string,
	step func(core.LifecycleComponent) func(context.Context) error) error {
	for _, c := range s.ordered() {
		if err := step(c)(ctx); err != nil {
			return fmt.Errorf("%s %s: %w", verb, name(c), err)
		}
	}
	return nil
}

func (s *Service) reverse(ctx context.Context, verb string,
	step func(core.LifecycleComponent) func(context.Context) error) error {
	seq := s.ordered()
	for i := len(seq) - 1; i >= 0; i-- {
		if err := step(seq[i])(ctx); err != nil {
			return fmt.Errorf("%s %s: %w", verb, name(seq[i]), err)
		}
	}
	return nil
}

// name labels a manager for an error message. It is a type switch rather than an
// interface method because these three are core-owned types this package already knows by
// name, and adding a method to the LifecycleComponent contract for the sake of an error
// string would oblige every implementation in the tree to carry it.
func name(c core.LifecycleComponent) string {
	switch c.(type) {
	case *rdb.RdbManager:
		return "the relational database manager"
	case *messaging.NatsManager:
		return "the broker manager"
	case *gqlcore.GraphQLManager:
		return "the GraphQL manager"
	}
	return "an unknown manager"
}
