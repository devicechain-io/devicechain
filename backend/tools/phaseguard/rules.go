// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package phaseguard

// Rules are the phase constraints this guard enforces.
//
// 🔴 THEY ARE ONE RULE TABLE RATHER THAN THREE GUARDS BECAUSE THEY ARE ONE CLASS. Each
// says the same thing in a different direction: some things a service builds must exist
// exactly once per process and some must be rebuilt on every start, and putting either
// in the wrong lifecycle phase breaks the SECOND start of a service that otherwise looks
// completely healthy. The engine — entry-point discovery from the framework's own
// contract, a types-resolved call graph, class-hierarchy expansion of interface calls —
// is identical for all of them; only the symbol list and the direction change. Splitting
// them into separate tools would mean three copies of the hard part and three chances
// for one copy to go blind.
var Rules = []Rule{
	{
		// core.HttpServer holds one *http.Server for the life of the instance and
		// nothing rebuilds it. net/http latches an http.Server's shutting-down flag
		// permanently, so a retained server binds a listener and then serves nothing;
		// HttpServer.Start refuses that outright instead, which makes the failure loud
		// and makes it a service that will not restart.
		Name: "http-server-per-start",
		Symbols: []Symbol{
			{Pkg: CorePkg, Recv: "Microservice", Name: "NewHttpServer"},
			{Pkg: CorePkg, Name: "NewHttpServerForHandler"},
			{Pkg: CorePkg, Name: "NewHttpServerForHandlerWithOptions"},
		},
		Forbidden: Initialize,
		Expect:    Start,
		// 🔴 A FLOOR WITH ROOM UNDER IT, NOT TODAY'S COUNT. Three servers are built on
		// the start path: the GraphQL manager's, core/service's probes-only server and
		// event-sources' device-ingest listener — two in core and one in event-sources —
		// all reached through a component's ExecuteStart. Two leaves room for merging a listener or retiring
		// the ingest transport without a failure that says nothing about the rule — and
		// a floor people edit to make green is a floor that stops meaning anything.
		//
		// There were five until the services with no GraphQL plane moved onto
		// core/service, and the floor used to argue that its sites spanned BOTH entry
		// shapes, so neither going blind could clear it. That is no longer true here:
		// every site is behind ExecuteStart. The Starter-callback door is still watched,
		// by the rules that have sites behind it — metrics-registered-once, where almost
		// every site is an Initializer-callback site, and start-stop-symmetry, which reads
		// nothing but callbacks. A door gone blind still fails the run, under those names.
		MinExpected: 2,
		Why: "core.HttpServer wraps one *http.Server for its lifetime, and an " +
			"http.Server cannot be restarted — one built on the initialize path is " +
			"retained across a stop and refuses the next start",
		Remedy: "build the server in ExecuteStart, or in the Starter callback, so each " +
			"start gets a fresh one and the bind error is returned to the caller",
	},
	{
		// The mirror image, and the reason this is a table. ServeMux.Handle panics on a
		// duplicate pattern, so a route registered per start takes the second start
		// down — and RegisterProbes mounts three of them at once.
		Name: "mux-registration-once",
		Symbols: []Symbol{
			{Pkg: CorePkg, Recv: "Microservice", Name: "RegisterProbes"},
		},
		Forbidden: Start,
		Expect:    Initialize,
		// 🔴 NO ROOM UNDER THIS FLOOR, ON PURPOSE. Two callers: the GraphQL manager
		// and core/service's probes-only server — the two ways a Service gets its probe
		// surface, both through ExecuteInitialize. No service registers probes itself any
		// more, so losing either site is not ordinary editing; it is the regression this
		// rule exists for, or a call graph that stopped resolving. The Initializer-callback
		// door has no site here and is watched by metrics-registered-once instead; see
		// http-server-per-start above.
		MinExpected: 2,
		Why: "http.ServeMux panics on a duplicate pattern and the microservice's mux " +
			"outlives a stop, so a route registered on the start path takes the " +
			"second start down",
		Remedy: "register routes in ExecuteInitialize, or in the Initializer callback, " +
			"which the lifecycle state machine runs at most once",
	},
	{
		// The same shape again for Prometheus. promauto registers on construction and
		// panics on a duplicate collector, so a metric built per start is a second
		// start that panics — the defect #1014 fixed by hand, with nothing to keep it
		// fixed.
		Name: "metrics-registered-once",
		Symbols: []Symbol{
			{Pkg: CorePkg, Recv: "Microservice", Name: "NewCounter"},
			{Pkg: CorePkg, Recv: "Microservice", Name: "NewCounterVec"},
			{Pkg: CorePkg, Recv: "Microservice", Name: "NewGauge"},
			{Pkg: CorePkg, Recv: "Microservice", Name: "NewGaugeVec"},
			{Pkg: CorePkg, Recv: "Microservice", Name: "NewProcessorMetrics"},
			// The general door: anything building its own collector reaches the
			// registry through this, and every constructor above is a convenience
			// wrapper around it.
			{Pkg: CorePkg, Recv: "Microservice", Name: "MetricsRegisterer"},
		},
		Forbidden:   Start,
		Expect:      Initialize,
		MinExpected: 3,
		Why: "these construct through promauto, which registers on construction and " +
			"panics on a duplicate collector, so a metric built per start panics on " +
			"the second start",
		Remedy: "build the instruments once on the initialize path and hand them to " +
			"whatever runs per start",
	},
}
