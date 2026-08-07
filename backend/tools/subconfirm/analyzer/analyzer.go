// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package analyzer implements subconfirm, a go/analysis pass that finds
// subscriptions the broker has never confirmed.
//
// # Why this is an ANALYZER and not a grep
//
// The defect it looks for is invisible to a textual search, and the attempt to find
// it textually is what produced a wrong answer the first time. `.Subscribe(` matches
// at least four unrelated APIs in this repo — nats.Conn, paho's mqtt.Client,
// nats.JetStreamContext and our own GraphQL-over-WebSocket client — and only two of
// them have the problem. A grep that flags all four is noise nobody keeps; a grep
// narrowed with `nc.` or `conn.` is a guess about variable names. The receiver's TYPE
// is the only thing that separates them, and only a type checker knows it.
//
// The same distinction cuts the other way and is the more useful half: JetStream's
// Subscribe round-trips to the JetStream API to create its consumer, so it is
// confirmed by construction and must NOT be reported. Textually it is identical to
// the call that is broken.
//
// # The two shapes
//
// Both are "an acknowledgement you did not wait for, or did not read".
//
//  1. nats.Conn.Subscribe and friends are ASYNCHRONOUS. They append a SUB to the
//     connection's write buffer and return. Until the server reads it the
//     subscription does not exist, and core NATS DROPS a publish with no subscriber
//     rather than queueing it — the message is not late, it is gone.
//
//  2. paho's mqtt.Client.Subscribe is acknowledged, but paho never turns a REFUSAL
//     into an error: its SUBACK handler copies the broker's return codes into the
//     token and completes it without calling setError. So a broker answering 0x80
//     ("subscription refused", typically an ACL denying read on the filter) yields a
//     token that waits successfully with Error() == nil. paho's own WaitTokenTimeout
//     shares the blind spot, which is how the second instance of it got written.
//
// # What satisfies it
//
// For NATS, either wrapper — messaging.SubscribeSynced or QueueSubscribeSynced — or
// a messaging.ConfirmSubscribed / Flush anywhere later in the same function, since
// one round trip confirms every subscription issued on the connection so far. For
// MQTT, messaging.SubscribeMqttConfirmed, which reads the granted QoS; there is no
// after-the-fact equivalent, because the answer arrives with the subscribe and is
// thrown away if nobody reads it.
//
// # Suppressing a report
//
// There are legitimate unconfirmed subscribes. The one that recurs is a SAME-
// CONNECTION subscribe followed by the publish it is waiting for: both writes land
// in one buffer under one lock, so the server reads SUB before PUB and a flush buys
// nothing but a round trip. Mark those with a directive carrying a reason:
//
//	//subconfirm:ok same connection publishes the request below, so SUB is ordered first
//	sub, err := nc.Subscribe(inbox, handler)
//
// The directive is accepted as a trailing comment on the call, or anywhere in the
// comment block that ends immediately above the enclosing statement — so a reason
// too long for one line can run on underneath it. A reason is
// required, and a directive that no longer suppresses anything is itself reported —
// a suppression that outlives its subject is how a guard quietly stops guarding.
//
// # Known limits
//
// Stated rather than left to be found. The pass matches a DECLARED type, so a type it
// cannot name escapes it: a method value (f := nc.Subscribe), or a call through a
// hand-declared local interface that redeclares the signature rather than embedding
// nats.Conn or mqtt.Client. It also reads position rather than execution order, so a
// deferred subscribe followed by a flush reads as confirmed. See the README for the
// full list and for the shapes that ARE covered (embedding, promotion, aliases).
package analyzer

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
	"golang.org/x/tools/go/types/typeutil"
)

// Directive suppresses one report. It must be followed by a reason.
const Directive = "//subconfirm:ok"

const (
	natsPkg = "github.com/nats-io/nats.go"
	mqttPkg = "github.com/eclipse/paho.mqtt.golang"
	// corePkg is where the confirmed wrappers live. Hard-coding our own import
	// path is deliberate: this is a house tool, and the alternative — accepting
	// any function called ConfirmSubscribed from anywhere — would let a local
	// no-op of that name switch the check off.
	corePkg = "github.com/devicechain-io/dc-microservice/messaging"
)

// rule matches one method set on one type, identified by the package PATH the type
// is declared in rather than by the local import name — an import alias, or a dot
// import, changes every spelling at the call site but none of this.
type rule struct {
	pkgPath string
	recv    string
	methods []string
	// flushable marks a rule whose subscribe can be confirmed AFTER the fact, by a
	// later call on the same connection. That is true of NATS — one flush confirms
	// every subscription issued so far — and false of MQTT, where the answer this
	// check is about arrives with the subscribe and cannot be re-read later.
	flushable bool
	// advise renders the diagnostic for one method of this rule.
	advise func(method string) string
}

var rules = []rule{
	{
		pkgPath: natsPkg,
		recv:    "Conn",
		// Every subscribe on the connection has the same problem, including the
		// SYNC variants: "sync" there describes how messages are delivered to the
		// caller (pulled from the Subscription rather than pushed to a callback),
		// not whether the server has acknowledged the SUB. They are the easiest
		// ones to misread.
		methods: []string{
			"Subscribe",
			"SubscribeSync",
			"QueueSubscribe",
			"QueueSubscribeSync",
			"QueueSubscribeSyncWithChan",
			"ChanSubscribe",
			"ChanQueueSubscribe",
		},
		flushable: true,
		advise: func(method string) string {
			return fmt.Sprintf("(*nats.Conn).%s returns before the server has registered the "+
				"subscription, and core NATS DROPS a publish that arrives with no subscriber — "+
				"the message is not late, it is gone. Use messaging.SubscribeSynced or "+
				"messaging.QueueSubscribeSynced, or call messaging.ConfirmSubscribed before "+
				"reporting this component started. If the SAME connection publishes what this "+
				"subscription is waiting for, the two are already ordered: say so with "+
				"`%s <reason>`.", method, Directive)
		},
	},
	{
		// EncodedConn wraps the same asynchronous SUB path. Nothing in this repo
		// uses it and the API is deprecated upstream, so this rule is defensive:
		// it is here so that reaching for it does not quietly bypass the check.
		// It carries no flushable exemption because EncodedConn's own Flush is a
		// distinct method that receiverName would not match to this receiver.
		pkgPath: natsPkg,
		recv:    "EncodedConn",
		methods: []string{"Subscribe", "QueueSubscribe"},
		advise: func(method string) string {
			return fmt.Sprintf("(*nats.EncodedConn).%s is the same asynchronous subscribe as "+
				"(*nats.Conn).%s and has the same consequence: core NATS DROPS a publish that "+
				"arrives before the server has registered the subscription. Nothing in this "+
				"repo uses EncodedConn — prefer the plain connection and "+
				"messaging.SubscribeSynced.", method, method)
		},
	},
	{
		pkgPath: mqttPkg,
		recv:    "Client",
		methods: []string{"Subscribe", "SubscribeMultiple"},
		advise: func(method string) string {
			return fmt.Sprintf("mqtt.Client.%s is acknowledged but paho never reports a REFUSAL: "+
				"its SUBACK handler copies the broker's return codes into the token without "+
				"calling setError, so a 0x80 answer waits successfully with Error() == nil — and "+
				"paho's own WaitTokenTimeout inherits that blind spot. Use "+
				"messaging.SubscribeMqttConfirmed, which reads the granted QoS. Waiting for an "+
				"ack is not the same as reading it.", method)
		},
	},
}

// Analyzer is the subconfirm pass. See the package comment for what it reports.
var Analyzer = &analysis.Analyzer{
	Name:     "subconfirm",
	Doc:      "report subscriptions the broker has never confirmed, and MQTT subscribes whose SUBACK is never read",
	URL:      "https://github.com/devicechain-io/devicechain/tree/main/backend/tools/subconfirm",
	Requires: []*analysis.Analyzer{inspect.Analyzer},
	Run:      run,
}

func run(pass *analysis.Pass) (any, error) {
	marks := collectDirectives(pass)

	insp := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)
	insp.WithStack([]ast.Node{(*ast.CallExpr)(nil)}, func(n ast.Node, push bool, stack []ast.Node) bool {
		if !push {
			return true
		}
		call := n.(*ast.CallExpr)
		r, method, ok := matchRule(pass, call)
		if !ok {
			return true
		}
		// Order matters: the directive is consulted first, so that marking a call
		// deliberately exempt still counts the directive as used even when a later
		// flush would have covered it anyway. Otherwise adding a flush elsewhere in
		// the function would turn a correct directive into a stale-directive report.
		if marks.suppress(pass, call, stack) {
			return true
		}
		if r.flushable && confirmedLater(pass, call, stack) {
			return true
		}
		pass.Reportf(call.Pos(), "%s", r.advise(method))
		return true
	})

	marks.reportUnused(pass)
	return nil, nil
}

// matchRule resolves the callee to its declaring package and receiver type. A call
// that resolves to nothing — an unresolved identifier in a package the type checker
// could not read — simply does not match, which is the right failure mode: this pass
// has no opinion about code that does not compile.
func matchRule(pass *analysis.Pass, call *ast.CallExpr) (rule, string, bool) {
	fn, _ := typeutil.Callee(pass.TypesInfo, call).(*types.Func)
	if fn == nil || fn.Pkg() == nil {
		return rule{}, "", false
	}
	recv, ok := receiverName(fn)
	if !ok {
		return rule{}, "", false
	}
	for _, r := range rules {
		if fn.Pkg().Path() != r.pkgPath || recv != r.recv {
			continue
		}
		for _, m := range r.methods {
			if fn.Name() == m {
				return r, m, true
			}
		}
	}
	return rule{}, "", false
}

// confirmedLater reports whether the enclosing function asks the server, ON THIS
// CONNECTION, for an acknowledgement after this subscribe.
//
// The diagnostic itself points at messaging.ConfirmSubscribed, so a pass that then
// reported its correct use would be telling people to do something it treats as
// wrong. The wrappers cover the common case; this covers the rest — ChanSubscribe,
// or several subscribes confirmed together by one round trip, which is what a flush
// actually does.
//
// The window is the whole enclosing function body rather than the next statement,
// because a flush confirms every subscription issued on the connection so far, so
// its position is not what makes it correct and demanding adjacency would reject the
// multi-subscribe shape this exists for. But "later in the body" is the only thing
// that stays loose. Two narrower conditions are load-bearing, and an earlier version
// of this that had neither accepted three confirmations that provably confirm
// nothing:
//
//   - SAME CONNECTION. A service commonly holds more than one — an ingest
//     connection and a control-plane one — and flushing the second says nothing
//     about a subscription on the first.
//   - NOT INSIDE A NESTED CLOSURE. A closure does not run where the subscribe runs,
//     and the worst case is circular: a flush inside the SUBSCRIBE'S OWN HANDLER,
//     which can only ever fire if the subscription was already confirmed.
func confirmedLater(pass *analysis.Pass, call *ast.CallExpr, stack []ast.Node) bool {
	body := enclosingBody(stack)
	if body == nil {
		return false
	}
	conn := receiverExpr(call)
	if conn == nil {
		return false
	}
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		if _, nested := n.(*ast.FuncLit); nested {
			return false
		}
		c, ok := n.(*ast.CallExpr)
		// Anything starting before this subscribe ends is either the subscribe
		// itself or one of its arguments.
		if !ok || c.Pos() < call.End() {
			return true
		}
		if target := confirmerTarget(pass, c); target != nil {
			found = sameExpr(pass, conn, target)
		}
		return true
	})
	return found
}

// confirmerTarget returns the expression naming the connection a call confirms, or
// nil if the call confirms nothing.
func confirmerTarget(pass *analysis.Pass, call *ast.CallExpr) ast.Expr {
	fn, _ := typeutil.Callee(pass.TypesInfo, call).(*types.Func)
	if fn == nil || fn.Pkg() == nil {
		return nil
	}
	if fn.Pkg().Path() == corePkg && fn.Name() == "ConfirmSubscribed" && len(call.Args) == 1 {
		return call.Args[0]
	}
	recv, ok := receiverName(fn)
	if !ok || fn.Pkg().Path() != natsPkg || recv != "Conn" {
		return nil
	}
	switch fn.Name() {
	case "Flush", "FlushTimeout", "FlushWithContext":
		return receiverExpr(call)
	}
	return nil
}

// receiverExpr is the expression a method was called on: the `nc` of nc.Subscribe.
func receiverExpr(call *ast.CallExpr) ast.Expr {
	sel, ok := unparen(call.Fun).(*ast.SelectorExpr)
	if !ok {
		return nil
	}
	return sel.X
}

// sameExpr reports whether two expressions denote the same connection.
//
// It handles the two spellings that occur — a local (`nc`) and a field path
// (`r.conn`, `nmgr.nc`) — by comparing the objects the type checker resolved, not
// the source text. Anything else (a call result, an index) answers NO, which errs
// toward reporting: a false positive here is a diagnostic on code somebody can look
// at, and a false negative is the defect this whole pass exists to catch.
func sameExpr(pass *analysis.Pass, a, b ast.Expr) bool {
	a, b = unparen(a), unparen(b)
	switch x := a.(type) {
	case *ast.Ident:
		y, ok := b.(*ast.Ident)
		if !ok {
			return false
		}
		obj := pass.TypesInfo.ObjectOf(x)
		return obj != nil && obj == pass.TypesInfo.ObjectOf(y)
	case *ast.SelectorExpr:
		y, ok := b.(*ast.SelectorExpr)
		return ok && sameExpr(pass, x.Sel, y.Sel) && sameExpr(pass, x.X, y.X)
	}
	return false
}

func unparen(e ast.Expr) ast.Expr {
	for {
		p, ok := e.(*ast.ParenExpr)
		if !ok {
			return e
		}
		e = p.X
	}
}

// enclosingBody is the body of the innermost function or closure containing the
// call. A closure gets its own body rather than its enclosing function's: a flush in
// the outer function does not run when the closure does.
func enclosingBody(stack []ast.Node) *ast.BlockStmt {
	for i := len(stack) - 1; i >= 0; i-- {
		switch f := stack[i].(type) {
		case *ast.FuncLit:
			return f.Body
		case *ast.FuncDecl:
			return f.Body
		}
	}
	return nil
}

// receiverName is the bare name of the type a method is declared on, with pointer
// and alias indirection stripped. It is empty for a plain function.
func receiverName(fn *types.Func) (string, bool) {
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() == nil {
		return "", false
	}
	t := types.Unalias(sig.Recv().Type())
	if p, ok := t.(*types.Pointer); ok {
		t = types.Unalias(p.Elem())
	}
	named, ok := t.(*types.Named)
	if !ok {
		return "", false
	}
	return named.Obj().Name(), true
}

// mark is one //subconfirm:ok directive.
type mark struct {
	pos  token.Pos
	line int
	// groupEnd is the last line of the comment block the directive belongs to. It
	// is what anchors a directive to the statement below, so that a reason too long
	// for one line can run on underneath the directive rather than in front of it.
	groupEnd int
	reason   string
	used     bool
}

// directives indexes every directive in the package by the file it appears in.
type directives struct {
	byFile map[string][]*mark
}

func collectDirectives(pass *analysis.Pass) *directives {
	d := &directives{byFile: map[string][]*mark{}}
	for _, f := range pass.Files {
		for _, group := range f.Comments {
			for _, c := range group.List {
				text := strings.TrimSpace(c.Text)
				if !strings.HasPrefix(text, Directive) {
					continue
				}
				p := pass.Fset.Position(c.Pos())
				m := &mark{
					pos:      c.Pos(),
					line:     p.Line,
					groupEnd: pass.Fset.Position(group.End()).Line,
					reason:   strings.TrimSpace(strings.TrimPrefix(text, Directive)),
				}
				if m.reason == "" {
					pass.Reportf(c.Pos(), "%s needs a reason: say why this subscription does "+
						"not need the broker's confirmation, so the next reader can tell a "+
						"deliberate exemption from a silenced warning", Directive)
					// Recorded anyway, so it still suppresses its call — one line
					// with one mistake should produce one report, not two.
				}
				d.byFile[p.Filename] = append(d.byFile[p.Filename], m)
			}
		}
	}
	return d
}

// suppress reports whether a directive covers this call, and marks it used.
//
// The accepted placements are a trailing comment on any line of the call, and
// ANYWHERE in the comment block that ends on the line immediately above the
// enclosing statement.
//
// 🔴 The block, not the line, is what anchors it, and getting that wrong is not a
// theoretical worry: the first version of this pass matched the directive's own line
// against stmt-1, and every reason in this repo too long for one line — which is most
// of them, since house comments run to paragraphs — silently stopped suppressing. It
// failed in the loudest possible way, reporting the call AND reporting the directive
// above it as dead, which is a gate nobody can satisfy. That double report is the
// only reason it was caught at all.
func (d *directives) suppress(pass *analysis.Pass, call *ast.CallExpr, stack []ast.Node) bool {
	marks := d.byFile[pass.Fset.Position(call.Pos()).Filename]
	if len(marks) == 0 {
		return false
	}
	stmtStart := pass.Fset.Position(enclosingStmt(stack).Pos()).Line
	callStart := pass.Fset.Position(call.Pos()).Line
	callEnd := pass.Fset.Position(call.End()).Line

	found := false
	for _, m := range marks {
		above := m.groupEnd == stmtStart-1
		// The TRAILING form is scoped to the matched call's own lines, not the
		// statement's. Scoped to the statement, one directive on the opening line of
		//
		//	consume(sub(nc.Subscribe("a", h)),
		//	        sub(nc.Subscribe("b", h)))
		//
		// silenced both — and pre-exempted a third that somebody adds later, which is
		// the whole-function over-reach this design refuses, shrunk to one statement.
		trailing := m.line >= callStart && m.line <= callEnd
		if above || trailing {
			m.used = true
			found = true
		}
	}
	return found
}

// enclosingStmt is the innermost statement containing the call, or the call itself
// if it has none — a package-level var initializer.
func enclosingStmt(stack []ast.Node) ast.Node {
	for i := len(stack) - 1; i >= 0; i-- {
		if s, ok := stack[i].(ast.Stmt); ok {
			return s
		}
	}
	return stack[len(stack)-1]
}

// reportUnused flags a directive that covered nothing. A directive with no reason
// has already been reported once; saying it is also unused adds nothing.
//
// It walks pass.Files rather than the index's map, so the reports come out in source
// order. Map iteration order is randomized, and a checker whose output reorders
// itself between runs is one nobody can diff.
func (d *directives) reportUnused(pass *analysis.Pass) {
	for _, f := range pass.Files {
		for _, m := range d.byFile[pass.Fset.Position(f.Pos()).Filename] {
			if m.used || m.reason == "" {
				continue
			}
			pass.Reportf(m.pos, "%s suppresses nothing here — the call it covered is gone or "+
				"already confirmed, so delete it", Directive)
		}
	}
}
