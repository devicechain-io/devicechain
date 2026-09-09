#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Refuses work that runs in the wrong lifecycle phase.
#
# 🔴 WHY THIS IS A GATE. core.LifecycleManager's own state allow lists say how often each
# step runs: initializeFrom is {Uninitialized}, so ExecuteInitialize runs AT MOST ONCE for
# a component instance, while startFrom is {Initialized, Stopped}, so ExecuteStart runs
# again after every stop. That splits everything a service builds in two, and putting
# either half in the wrong phase breaks the SECOND start of a service that looks entirely
# healthy on its first:
#
#   - core.HttpServer holds one *http.Server for its lifetime and nothing rebuilds it.
#     net/http latches an http.Server's shutting-down flag permanently, so a server
#     retained across a stop binds a listener and then serves nothing. HttpServer.Start
#     refuses that outright instead — which is why this class fails loudly rather than
#     silently — but the service still does not restart.
#   - http.ServeMux panics on a duplicate pattern, and the microservice's mux outlives a
#     stop. A route registered per start takes the second start down.
#   - promauto registers a collector on construction and panics on a duplicate. Same
#     shape, same second start.
#
# All three sit correctly today and nothing but a comment says they have to. This
# repository has repeatedly found that a claim in a comment outruns what any test can see.
#
# 🔴 WHY IT IS ONE CHECK AND NOT THREE. They are one class stated in two directions —
# build-once must be on the initialize path, build-per-start must not be — over one hard
# mechanism: deciding which phase a piece of work runs in. Three tools would mean three
# copies of that mechanism and three chances for one copy to go blind.
#
# 🔴 WHY IT FOLLOWS A CALL GRAPH RATHER THAN MATCHING NAMES. The phase a function runs in
# is a property of its CALLER. Three shapes in this tree prove it, and each defeats a
# different shortcut: createNatsComponents reads like initialization and is invoked on
# START; user-management's initializer callback is an ANONYMOUS CLOSURE with no name to
# key on at all; and device-management's InboundEventsProcessor is reached through
# INTERFACE DISPATCH, which no statically-named walk follows. So the analyzer starts at
# the entry points the framework itself defines — the LifecycleComponent method for the
# phase, and the Preprocess/Postprocess pair of that phase's LifecycleCallback — and
# follows the call graph out of them, resolving every callee through go/types and
# expanding interface calls by class-hierarchy analysis.
#
# 🔴 WHY IT PARSES RATHER THAN GREPS. The watched names are already written down in this
# tree in PROSE: core/core/http.go's doc comments name NewHttpServer and
# NewHttpServerForHandler, graphql.go explains in a comment why it builds a fresh server
# per start, and RegisterProbes documents that it must be called at most once. A text scan
# reports all of that on its first run and is then narrowed with exclusions until it is
# quiet and wrong. A type-checked call graph never sees a comment, and it makes an import
# alias, a dot-import, a line break and a method value that is never called all the same
# match.
#
# ⚠️ WHAT IT CANNOT SEE, stated here rather than left for a reviewer to find. A narrow
# true claim beats a broad false one:
#
#   - A CALL THROUGH A VALUE OF FUNC TYPE that never names the function: a func stored in
#     a struct field, put in a map, or received over a channel. Naming the function
#     anywhere in a reachable body IS an edge, so `f := build; f()` is caught; assembling
#     the same thing out of values built elsewhere is not. Closing it needs full
#     value-flow analysis, which would also start guessing.
#   - REFLECTION, and any function obtained at run time. It still has to name the symbol
#     somewhere to get a value, so the common shapes are caught.
#   - Interface implementations OUTSIDE the loaded program. The class hierarchy is built
#     from named types in the workspace's own packages, so a dependency's type that
#     implements one of our interfaces is not expanded to.
#   - FILES EXCLUDED BY BUILD CONSTRAINTS for the platform the check runs on. This loads a
#     build rather than parsing syntax, which is what buys the type resolution, and the
#     price is that a GOOS-guarded file is invisible on the other GOOS. CI runs on linux.
#   - _test.go FILES, which are not loaded. A component built only by a test is not a
#     component the lifecycle runs.
#   - A component instance REBUILT PER START — a start callback that constructs a fresh
#     component and initializes it. Its ExecuteInitialize genuinely runs once per start,
#     so a per-start construction there is correct and would be reported. Nothing in the
#     tree does this today; if something starts to, it needs an allow list rather than a
#     wider rule, because the rule is right and the arrangement is the exception.
#   - The reverse phase boundary is a deliberate STOP, not a hole: the initialize
#     traversal halts at any start-phase entry point and vice versa. Work under an
#     ExecuteStart runs again on every start whatever else also reached it, so it is
#     governed by the other direction of the same table.
#   - Only the modules go.work declares are loaded. A Go tree outside the workspace is out
#     of scope, which is why the module list is derived from go.work rather than written
#     out here.
#
# Recorded as NOT holes, so nobody re-tests them: an import alias, a dot-import, a call
# split across lines, an unkeyed LifecycleCallbacks literal, a method value that is never
# called, an indirect call through a local variable, a goroutine, a defer, a generated
# file, a //nolint comment, and a function whose NAME says the opposite of its phase. Each
# is a self-test fixture below.
#
#   hack/check-lifecycle-phase.sh
#   hack/check-lifecycle-phase.sh --self-test   # prove the check can fail
#
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
BIN="$TMP/phaseguard"

# Built from source every run. A checked-in binary is a guard that stops tracking the rule
# it enforces the moment somebody edits the analyzer.
#
# 🔴 THE BINARY IS BUILT AND RUN, NEVER `go run`. `go run` reports the compiler's exit
# status for a build failure and the program's otherwise, but it collapses nothing it
# cannot distinguish — and more to the point it exits 1 on its own errors, which is
# exactly this tool's "findings" code. A three-valued gate whose 2 can be produced by the
# launcher is a two-valued gate.
GOWORK=off go build -C backend/tools/phaseguard -o "$BIN" ./cmd/phaseguard

# ---------------------------------------------------------------------------
# The fake framework the self-test fixtures are checked against.
# ---------------------------------------------------------------------------
# 🔴 THE FIXTURES TYPE-CHECK AGAINST A REAL PACKAGE, AND THEY HAVE TO. This analyzer
# resolves every callee through go/types, so a fixture that does not type-check is a
# fixture it refuses to analyse at all — which would make every self-test shape pass by
# exiting 2 and prove nothing. So each fixture is a self-contained module carrying a
# miniature core: the same lifecycle types and the same constructor names under an import
# path of its own, with no dependencies, which `go list` resolves offline.
fake_core() {
  cat <<'EOF'
// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import "context"

type LifecycleCallback struct {
	Preprocess  func(context.Context) error
	Postprocess func(context.Context) error
}

type LifecycleCallbacks struct {
	Initializer LifecycleCallback
	Starter     LifecycleCallback
	Stopper     LifecycleCallback
	Terminator  LifecycleCallback
}

type LifecycleComponent interface {
	ExecuteInitialize(context.Context) error
	ExecuteStart(context.Context) error
}

type HttpServer struct{}

type HttpServerOptions struct{}

type Microservice struct{}

func (ms *Microservice) NewHttpServer(port int32) *HttpServer { return nil }

func NewHttpServerForHandler(port int32, handler any) *HttpServer { return nil }

func NewHttpServerForHandlerWithOptions(port int32, handler any, opts HttpServerOptions) *HttpServer {
	return nil
}

func (ms *Microservice) RegisterProbes(gate any) {}

func (ms *Microservice) NewCounter(name, help string, labels []string) any { return nil }

func (ms *Microservice) NewCounterVec(name, help string, labels []string) any { return nil }

func (ms *Microservice) NewGauge(name, help string, labels []string) any { return nil }

func (ms *Microservice) NewGaugeVec(name, help string, labels []string) any { return nil }

func (ms *Microservice) NewProcessorMetrics(name string) any { return nil }

func (ms *Microservice) MetricsRegisterer() any { return nil }
EOF
}

self_test() {
  local fx="$TMP/fx" rc=0
  export GOWORK=off

  # plant <name> <svc-source>  — one module per shape.
  #
  # 🔴 ONE FIXTURE PER SHAPE, NOT ONE FIXTURE WITH EVERYTHING IN IT. A combined fixture
  # is satisfied by a checker that detects any single shape, which is how a guard ends up
  # enforcing a fraction of what it claims while every run stays green.
  plant() {
    mkdir -p "$fx/$1/core" "$fx/$1/svc"
    printf 'module dcfx\n\ngo 1.24\n' >"$fx/$1/go.mod"
    fake_core >"$fx/$1/core/core.go"
    printf '%s\n' "$2" >"$fx/$1/svc/x.go"
  }

  # run <shape> [extra flags...] -> prints combined output, returns the exit status
  run() {
    local shape="$1"
    shift
    "$BIN" -dir="$fx/$shape" -core-pkg=dcfx/core "$@" ./... 2>&1
  }

  # ---------------------------------------------------------------------
  # Shapes that MUST be caught. Every one of them compiles.
  # ---------------------------------------------------------------------

  # 1. the plain shape: constructed directly in a component's ExecuteInitialize.
  plant direct 'package svc

import (
	"context"

	"dcfx/core"
)

type C struct{ ms *core.Microservice }

func (c *C) ExecuteInitialize(context.Context) error {
	_ = c.ms.NewHttpServer(8080)
	return nil
}

func (c *C) ExecuteStart(context.Context) error { return nil }'

  # 2. one hop through a helper — the shape a call-site-only check misses.
  plant helper 'package svc

import (
	"context"

	"dcfx/core"
)

type C struct{ ms *core.Microservice }

func (c *C) ExecuteInitialize(context.Context) error { return c.build() }

func (c *C) build() error {
	_ = c.ms.NewHttpServer(8080)
	return nil
}

func (c *C) ExecuteStart(context.Context) error { return nil }'

  # 3. 🔴 THE ANONYMOUS CLOSURE. user-management writes its initializer callback exactly
  #    like this. There is no name to key on even in principle, so a name-based sweep
  #    cannot reach it and a guard that misses this misses the shape most likely to be
  #    written next.
  plant closure 'package svc

import (
	"context"

	"dcfx/core"
)

var MS *core.Microservice

func Build() core.LifecycleCallbacks {
	return core.LifecycleCallbacks{
		Initializer: core.LifecycleCallback{
			Postprocess: func(context.Context) error {
				_ = MS.NewHttpServer(8080)
				return nil
			},
		},
	}
}'

  # 4. a NAMED function assigned to the initializer pair. It is never called by name
  #    anywhere; the assignment is the only statement of its phase.
  plant namedcallback 'package svc

import (
	"context"

	"dcfx/core"
)

var MS *core.Microservice

func afterInit(context.Context) error {
	_ = MS.NewHttpServer(8080)
	return nil
}

func Build() core.LifecycleCallbacks {
	return core.LifecycleCallbacks{
		Initializer: core.LifecycleCallback{Preprocess: afterInit},
	}
}'

  # 5. 🔴 THE NAME LIES. A function called startHttpServer, invoked from
  #    ExecuteInitialize. Anything that classified by name would put this in the start
  #    phase and report clean. Its counterweight — a function called initializeX invoked
  #    from ExecuteStart, which must NOT be flagged — is fixture C2 below.
  plant misleadingname 'package svc

import (
	"context"

	"dcfx/core"
)

type C struct{ ms *core.Microservice }

func (c *C) ExecuteInitialize(context.Context) error { return c.startHttpServer() }

func (c *C) startHttpServer() error {
	_ = c.ms.NewHttpServer(8080)
	return nil
}

func (c *C) ExecuteStart(context.Context) error { return nil }'

  # 6. 🔴 INTERFACE DISPATCH. ExecuteInitialize calls through an interface; the
  #    implementation constructs. No walk that follows only statically-named callees
  #    arrives here, and this is how device-management's processor is reached.
  plant interfacedispatch 'package svc

import (
	"context"

	"dcfx/core"
)

type Builder interface{ Build() error }

type impl struct{ ms *core.Microservice }

func (i *impl) Build() error {
	_ = i.ms.NewHttpServer(8080)
	return nil
}

type C struct{ b Builder }

func (c *C) ExecuteInitialize(context.Context) error { return c.b.Build() }

func (c *C) ExecuteStart(context.Context) error { return nil }'

  # 7. an import alias, which defeats anything hardcoding the name "core".
  plant importalias 'package svc

import (
	"context"

	dc "dcfx/core"
)

type C struct{ ms *dc.Microservice }

func (c *C) ExecuteInitialize(context.Context) error {
	_ = c.ms.NewHttpServer(8080)
	return nil
}

func (c *C) ExecuteStart(context.Context) error { return nil }'

  # 8. a dot-import: no package qualifier survives at the call site at all.
  plant dotimport 'package svc

import (
	"context"

	. "dcfx/core"
)

type C struct{ ms *Microservice }

func (c *C) ExecuteInitialize(context.Context) error {
	_ = NewHttpServerForHandler(8080, nil)
	return nil
}

func (c *C) ExecuteStart(context.Context) error { return nil }'

  # 9. split across lines — gofmt keeps this, and a grep for `.NewHttpServer(` misses it.
  plant linesplit 'package svc

import (
	"context"

	"dcfx/core"
)

type C struct{ ms *core.Microservice }

func (c *C) ExecuteInitialize(context.Context) error {
	_ = c.ms.
		NewHttpServer(8080)
	return nil
}

func (c *C) ExecuteStart(context.Context) error { return nil }'

  # 10. a method VALUE. Nothing is called here at all; the call happens later, possibly
  #     in another file, where nothing watched is named.
  plant methodvalue 'package svc

import (
	"context"

	"dcfx/core"
)

type C struct {
	ms    *core.Microservice
	build func(int32) *core.HttpServer
}

func (c *C) ExecuteInitialize(context.Context) error {
	c.build = c.ms.NewHttpServer
	return nil
}

func (c *C) ExecuteStart(context.Context) error { return nil }'

  # 11. the indirect call, which is what shape 10 buys you.
  plant indirect 'package svc

import (
	"context"

	"dcfx/core"
)

type C struct{ ms *core.Microservice }

func (c *C) ExecuteInitialize(context.Context) error {
	fn := c.ms.NewHttpServer
	_ = fn(8080)
	return nil
}

func (c *C) ExecuteStart(context.Context) error { return nil }'

  # 12. an UNKEYED LifecycleCallbacks literal. It assigns the same fields and names none
  #     of them, so a reader that only understands keyed literals sees no callbacks and
  #     reports the file clean.
  plant positional 'package svc

import (
	"context"

	"dcfx/core"
)

var MS *core.Microservice

func afterInit(context.Context) error {
	_ = MS.NewHttpServer(8080)
	return nil
}

func noop(context.Context) error { return nil }

func Build() core.LifecycleCallbacks {
	return core.LifecycleCallbacks{
		core.LifecycleCallback{Preprocess: afterInit, Postprocess: noop},
		core.LifecycleCallback{Preprocess: noop, Postprocess: noop},
		core.LifecycleCallback{Preprocess: noop, Postprocess: noop},
		core.LifecycleCallback{Preprocess: noop, Postprocess: noop},
	}
}'

  # 13. a goroutine spawned from ExecuteInitialize. It still runs once per component,
  #     and the server it builds is still retained across a stop.
  plant goroutine 'package svc

import (
	"context"

	"dcfx/core"
)

type C struct{ ms *core.Microservice }

func (c *C) ExecuteInitialize(context.Context) error {
	go func() { _ = c.ms.NewHttpServer(8080) }()
	return nil
}

func (c *C) ExecuteStart(context.Context) error { return nil }'

  # 14. a defer.
  plant deferred 'package svc

import (
	"context"

	"dcfx/core"
)

type C struct{ ms *core.Microservice }

func (c *C) build() { _ = c.ms.NewHttpServer(8080) }

func (c *C) ExecuteInitialize(context.Context) error {
	defer c.build()
	return nil
}

func (c *C) ExecuteStart(context.Context) error { return nil }'

  # 15. a generated file. "DO NOT EDIT" is not "do not check".
  plant generated '// Code generated by gen. DO NOT EDIT.

package svc

import (
	"context"

	"dcfx/core"
)

type C struct{ ms *core.Microservice }

func (c *C) ExecuteInitialize(context.Context) error {
	_ = c.ms.NewHttpServer(8080)
	return nil
}

func (c *C) ExecuteStart(context.Context) error { return nil }'

  # 16. a //nolint-shaped escape, which this guard does not honour.
  plant nolint 'package svc

import (
	"context"

	"dcfx/core"
)

type C struct{ ms *core.Microservice }

//nolint:all // deliberate
func (c *C) ExecuteInitialize(context.Context) error {
	_ = c.ms.NewHttpServer(8080)
	return nil
}

func (c *C) ExecuteStart(context.Context) error { return nil }'

  # 17. three hops, to prove the traversal is transitive rather than one level deep.
  plant deepchain 'package svc

import (
	"context"

	"dcfx/core"
)

type C struct{ ms *core.Microservice }

func (c *C) ExecuteInitialize(context.Context) error { return c.a() }
func (c *C) a() error                                { return c.b() }
func (c *C) b() error                                { return c.d() }
func (c *C) d() error {
	_ = core.NewHttpServerForHandlerWithOptions(8080, nil, core.HttpServerOptions{})
	return nil
}

func (c *C) ExecuteStart(context.Context) error { return nil }'

  # 18. the OTHER DIRECTION: a route registered on the start path. ServeMux panics on a
  #     duplicate pattern, so this is the second start crashing.
  plant muxonstart 'package svc

import (
	"context"

	"dcfx/core"
)

type C struct{ ms *core.Microservice }

func (c *C) ExecuteInitialize(context.Context) error { return nil }

func (c *C) ExecuteStart(context.Context) error {
	c.ms.RegisterProbes(nil)
	return nil
}'

  # 19. a Prometheus collector built on the start path. promauto panics on a duplicate.
  plant metricsonstart 'package svc

import (
	"context"

	"dcfx/core"
)

type C struct{ ms *core.Microservice }

func (c *C) ExecuteInitialize(context.Context) error { return nil }

func (c *C) ExecuteStart(context.Context) error {
	_ = c.ms.NewCounterVec("x", "help", nil)
	return nil
}'

  # 20. the same, reached through the START callback closure rather than a component —
  #     the mirror of shape 3, so neither door is proven by the other.
  plant metricsinstartcallback 'package svc

import (
	"context"

	"dcfx/core"
)

var MS *core.Microservice

func Build() core.LifecycleCallbacks {
	return core.LifecycleCallbacks{
		Starter: core.LifecycleCallback{
			Postprocess: func(context.Context) error {
				_ = MS.NewProcessorMetrics("x")
				return nil
			},
		},
	}
}'

  # 21. the registry handed out directly, which is how anything builds its own collector
  #     without naming one of the named constructors above it.
  plant registereronstart 'package svc

import (
	"context"

	"dcfx/core"
)

type C struct{ ms *core.Microservice }

func (c *C) ExecuteInitialize(context.Context) error { return nil }

func (c *C) ExecuteStart(context.Context) error {
	_ = c.ms.MetricsRegisterer()
	return nil
}'

  for shape in direct helper closure namedcallback misleadingname interfacedispatch \
               importalias dotimport linesplit methodvalue indirect positional \
               goroutine deferred generated nolint deepchain muxonstart \
               metricsonstart metricsinstartcallback registereronstart; do
    local out status=0
    out="$(run "$shape" -liveness=false)" || status=$?
    if [ "$status" -ne 1 ]; then
      echo "self-test: shape '$shape' exited $status, want 1 — the guard does not catch it" >&2
      echo "$out" >&2
      rc=1
      continue
    fi
    # Naming the file and line is the difference between a gate and an alarm.
    if ! grep -q "$fx/$shape/svc/x.go:[0-9]" <<<"$out"; then
      echo "self-test: shape '$shape' failed without naming a file and line:" >&2
      echo "$out" >&2
      rc=1
    fi
  done

  # ---------------------------------------------------------------------
  # The counterweights. A guard that flags correct code is one nobody keeps, and three
  # of these are the exact false positives the design had to be shaped around.
  # ---------------------------------------------------------------------

  # C1. the correct arrangement, which is what every service in the tree does today:
  #     routes and metrics at initialize, a fresh server at start.
  plant clean 'package svc

import (
	"context"

	"dcfx/core"
)

type C struct {
	ms  *core.Microservice
	srv *core.HttpServer
}

func (c *C) ExecuteInitialize(context.Context) error {
	c.ms.RegisterProbes(nil)
	_ = c.ms.NewCounterVec("x", "help", nil)
	return nil
}

func (c *C) ExecuteStart(context.Context) error {
	c.srv = c.ms.NewHttpServer(8080)
	return nil
}'

  # C2. 🔴 THE NAME LIES THE OTHER WAY: initializeHttpServer, invoked from ExecuteStart.
  #     A guard that classified by name would report this as a defect, and this is the
  #     shape that actually exists in the tree — createNatsComponents.
  plant misleadingnameok 'package svc

import (
	"context"

	"dcfx/core"
)

type C struct {
	ms  *core.Microservice
	srv *core.HttpServer
}

func (c *C) ExecuteInitialize(context.Context) error { return nil }

func (c *C) ExecuteStart(context.Context) error { return c.initializeHttpServer() }

func (c *C) initializeHttpServer() error {
	c.srv = c.ms.NewHttpServer(8080)
	return nil
}'

  # C3. 🔴 THE PROSE CASE, which is real code in this tree and is what defeats a grep:
  #     core/core/http.go and core/graphql/graphql.go both explain these constructors in
  #     comments, and one of those comments says the words "ExecuteInitialize" and
  #     "NewHttpServer" in the same sentence.
  plant prose 'package svc

// NewHttpServer builds the server. Do not call NewHttpServerForHandler or
// NewHttpServerForHandlerWithOptions from ExecuteInitialize: the server would be
// retained across a stop. RegisterProbes and NewCounterVec go the other way.
const note = "see NewHttpServer and RegisterProbes"

func Doc() string { return note }'

  # C4. 🔴 A STARTER CLOSURE WRITTEN LEXICALLY INSIDE THE INITIALIZER CALLBACK. Where a
  #     closure is TYPED is not the phase it RUNS in, and a traversal that descended into
  #     it from its lexical parent would report every service that wires its sub-component
  #     callbacks during initialization. This is the carve-out the analyzer makes, and
  #     without this fixture nothing would notice if it were removed.
  plant starterinsideinitializer 'package svc

import (
	"context"

	"dcfx/core"
)

var MS *core.Microservice

var Sub core.LifecycleCallbacks

func Build() core.LifecycleCallbacks {
	return core.LifecycleCallbacks{
		Initializer: core.LifecycleCallback{
			Postprocess: func(context.Context) error {
				Sub = core.LifecycleCallbacks{
					Starter: core.LifecycleCallback{
						Postprocess: func(context.Context) error {
							_ = MS.NewHttpServer(8080)
							return nil
						},
					},
				}
				return nil
			},
		},
	}
}'

  # C5. 🔴 THE PHASE BOUNDARY, WALKED THE WRONG WAY. A component whose Start is invoked
  #     from the initialize path would otherwise drag every ExecuteStart in the program
  #     into the initialize phase through the interface expansion — and then every
  #     legitimate per-start construction in the workspace reports as a defect at once.
  #     The traversal stops at the other phase's entry points instead, and stopping is
  #     sound: work under an ExecuteStart runs again on every start whatever else also
  #     reached it, so it is governed by the other direction of the same table.
  #
  #     This fixture exists because the barrier SURVIVED a mutation without it: nothing
  #     in the workspace reaches a start entry point from the initialize path today, so
  #     removing the barrier changed no answer and no assertion noticed. A survivor names
  #     a missing input class, not a missing assertion.
  plant phaseboundary 'package svc

import (
	"context"

	"dcfx/core"
)

// manager mirrors what core.LifecycleManager does: it holds the component behind the
// interface and calls the phase method through it.
type manager struct{ comp core.LifecycleComponent }

func (m *manager) Start(ctx context.Context) error { return m.comp.ExecuteStart(ctx) }

type Sub struct {
	ms  *core.Microservice
	srv *core.HttpServer
}

func (s *Sub) ExecuteInitialize(context.Context) error { return nil }

func (s *Sub) ExecuteStart(context.Context) error {
	s.srv = s.ms.NewHttpServer(8080)
	return nil
}

type C struct{ mgr *manager }

func (c *C) ExecuteInitialize(ctx context.Context) error { return c.mgr.Start(ctx) }

func (c *C) ExecuteStart(context.Context) error { return nil }'

  local status out
  for shape in clean misleadingnameok prose starterinsideinitializer phaseboundary; do
    status=0
    out="$(run "$shape" -liveness=false)" || status=$?
    if [ "$status" -ne 0 ]; then
      echo "self-test: the '$shape' fixture exited $status, want 0 — the guard flags something legitimate" >&2
      echo "$out" >&2
      rc=1
    fi
  done

  # ---------------------------------------------------------------------
  # The instrument checks. Every one of these must exit 2, not 0 and not 1.
  # ---------------------------------------------------------------------

  # E1. a scan that read nothing must FAIL, not report clean.
  mkdir -p "$fx/empty"
  printf 'module dcfx\n\ngo 1.24\n' >"$fx/empty/go.mod"
  status=0
  out="$(run empty -liveness=false)" || status=$?
  if [ "$status" -ne 2 ]; then
    echo "self-test: an empty tree exited $status, want 2 — a scan that read nothing must not report clean" >&2
    echo "$out" >&2
    rc=1
  fi

  # E2. 🔴 A RENAMED CONSTRUCTOR. Every reference to it silently stops matching, which
  #     drives the finding count to zero and is indistinguishable from a clean tree by any
  #     other measure. Asking whether the symbol still EXISTS is what turns that into an
  #     instrument failure rather than a pass.
  plant renamed 'package svc

import "context"

type C struct{}

func (c *C) ExecuteInitialize(context.Context) error { return nil }
func (c *C) ExecuteStart(context.Context) error      { return nil }'
  # shellcheck disable=SC2016
  sed -i 's/func (ms \*Microservice) NewHttpServer(/func (ms *Microservice) NewHttpListener(/' \
    "$fx/renamed/core/core.go"
  status=0
  out="$(run renamed -liveness=false)" || status=$?
  if [ "$status" -ne 2 ]; then
    echo "self-test: a renamed constructor exited $status, want 2 — a symbol that stopped existing is not a clean tree" >&2
    echo "$out" >&2
    rc=1
  elif ! grep -q "does not exist in the loaded program" <<<"$out"; then
    echo "self-test: the renamed-symbol failure did not say what was missing:" >&2
    echo "$out" >&2
    rc=1
  fi

  # E3. 🔴 THE LIVENESS PROBE ITSELF, run ARMED against a tree whose sites it cannot
  #     reach. Zero findings is what a clean tree reports AND what a matcher that has
  #     gone blind reports; the only thing that separates them is whether the guard can
  #     still see the sites it is supposed to see. Here the clean fixture has one server
  #     on the start path and the rule's floor is three, so an armed run must refuse.
  status=0
  out="$(run clean)" || status=$?
  if [ "$status" -ne 2 ]; then
    echo "self-test: an armed run over a tree short of its expected sites exited $status, want 2" >&2
    echo "$out" >&2
    rc=1
  elif ! grep -q "is not a pass" <<<"$out"; then
    echo "self-test: the liveness failure did not say what was short:" >&2
    echo "$out" >&2
    rc=1
  fi

  # E4. a tree with the framework present and no lifecycle wiring at all. Phase discovery
  #     that stopped recognising the framework's contract looks exactly like this, and it
  #     makes every rule report clean at once.
  status=0
  out="$(run prose -min-initialize-entries=1 -min-start-entries=1)" || status=$?
  if [ "$status" -ne 2 ]; then
    echo "self-test: a tree with no lifecycle entry points exited $status, want 2" >&2
    echo "$out" >&2
    rc=1
  elif ! grep -q "entry points" <<<"$out"; then
    echo "self-test: the entry-point failure did not say what was missing:" >&2
    echo "$out" >&2
    rc=1
  fi

  # E5. 🔴 A TREE THAT DOES NOT TYPE-CHECK. Every resolution here comes from the type
  #     checker, so a package that failed to check yields a call graph with silent holes —
  #     and holes report as clean. It must be refused, and it must NOT be reported as a
  #     finding either, because it is not one.
  plant broken 'package svc

import (
	"context"

	"dcfx/core"
)

type C struct{ ms *core.Microservice }

func (c *C) ExecuteInitialize(context.Context) error {
	_ = c.ms.NewHttpServer(8080)
	return undefinedIdentifier
}

func (c *C) ExecuteStart(context.Context) error { return nil }'
  status=0
  out="$(run broken -liveness=false)" || status=$?
  if [ "$status" -ne 2 ]; then
    echo "self-test: a tree that does not type-check exited $status, want 2" >&2
    echo "$out" >&2
    rc=1
  fi

  if [ "$rc" -ne 0 ]; then
    echo "self-test FAILED" >&2
    exit 1
  fi
  echo "self-test passed: 21 evasion shapes caught across all three rules, five legitimate" \
       "arrangements allowed (including the prose about them, a start callback written" \
       "inside an initializer, and a component started from the initialize path), and" \
       "five instrument failures refused with exit 2."
}

case "${1-}" in
  --self-test)
    self_test
    exit 0
    ;;
  "") ;;
  *)
    echo "usage: $0 [--self-test]" >&2
    exit 2
    ;;
esac

# ---------------------------------------------------------------------------
# The real run.
# ---------------------------------------------------------------------------
# The module list comes from go.work through `go list -m`, so a service added tomorrow is
# covered the day it lands and nothing here has to be kept in step by hand.
#
# 🔴 IT IS CROSS-CHECKED AGAINST A SECOND, INDEPENDENT READER. A derived list fails
# SILENTLY: an empty or truncated one scans nothing and reports clean, which is the
# absence-read-as-an-answer this repository keeps hitting. hack/list-workspace-modules.sh
# parses go.work with awk and shares no code with `go list`, so the two agreeing is
# evidence rather than a restatement.
mapfile -t PATTERNS < <(go list -m -f '{{.Dir}}/...')
declared="$(hack/list-workspace-modules.sh | wc -l)"
if [ "${#PATTERNS[@]}" -ne "$declared" ]; then
  echo "check-lifecycle-phase: go list -m reported ${#PATTERNS[@]} modules but go.work declares $declared;" \
       "the module list is wrong, so a clean result from it means nothing" >&2
  exit 2
fi
if [ "${#PATTERNS[@]}" -lt 20 ]; then
  echo "check-lifecycle-phase: only ${#PATTERNS[@]} workspace modules resolved, expected at least 20" >&2
  exit 2
fi

# The floors below are floors with room under them, not tracking counts. The workspace
# parsed 191 packages and 912 Go files when this landed, with 52 entry points in each
# phase; a floor set at today's number is a floor somebody edits to make green.
"$BIN" -dir=. \
  -min-packages=100 -min-files=500 \
  -min-initialize-entries=20 -min-start-entries=20 \
  "${PATTERNS[@]}"
