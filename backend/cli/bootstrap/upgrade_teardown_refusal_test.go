// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/config"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
)

// 🔴 A SAME-VERSION `dcctl upgrade` IS THE HOLE, AND IT IS NOT A CORNER CASE. A
// version-CHANGING upgrade is already refused over a teardown: recordUpgradedVersion
// reaches writeInstanceCR, which refuses a declaration reading Destroying. But the same
// function returns EARLY when the declaration already names the version this run is
// moving to — a flagless re-run, the most ordinary thing an operator types after a run
// that failed — so it never reaches that refusal, while the deferred terminal stamp
// registered just after it still fires. The result is a `dcctl upgrade` that ERASES the
// record of an unfinished teardown and declares the instance Ready: the precise state
// this whole change exists to make visible, removed by the command meant to be harmless.
//
// So the refusal is made in hydrateUpgradeState, which runs before any of that.

// stubDeployedInstance makes the configuration-document read answer, so a hydration that
// gets PAST the teardown refusal fails somewhere recognisably later instead of reaching
// for a cluster.
func stubDeployedInstance(t *testing.T, cfg *config.InstanceConfiguration) {
	t.Helper()
	orig := lookupDeployedInstance
	t.Cleanup(func() { lookupDeployedInstance = orig })
	lookupDeployedInstance = func(context.Context, string, string) (*config.InstanceConfiguration, error) {
		return cfg, nil
	}
}

// stubDeclaration makes the declaration read answer with a fixed instance.
func stubDeclaration(t *testing.T, inst *dcv1beta1.Instance) {
	t.Helper()
	orig := readInstanceDeclaration
	t.Cleanup(func() { readInstanceDeclaration = orig })
	readInstanceDeclaration = func(context.Context, string, string) (*dcv1beta1.Instance, error) {
		return inst, nil
	}
}

func TestAnUpgradeRefusesAnInstanceWhoseTeardownDidNotFinish(t *testing.T) {
	provider, err := Get("local")
	if err != nil {
		t.Fatal(err)
	}
	binding := ClusterBinding{KubeContext: "kind-c", Cluster: "c"}
	opts := UpgradeOptions{Options: Options{Instance: "prod"}}
	// The operator guard runs first and reads a cluster; these tests are about the
	// refusals that come after it.
	stubOperatorCheck(t, nil)

	// 🔴 BOTH HALVES OF THE EVIDENCE, BECAUSE EITHER CAN BE THE ONLY ONE THERE. The local
	// marker is written by every destroy, including one that never reached the cluster;
	// the phase is the only half an operator on a DIFFERENT machine can see. A refusal
	// wired to one of them is silent for the other's case.
	t.Run("the local destroy marker says so", func(t *testing.T) {
		fakeHome(t)
		if err := writeDestroyMarker("prod"); err != nil {
			t.Fatal(err)
		}
		stubDeclaration(t, atVersion(t, "ghcr.io/devicechain-io", "v1.3.0")) // phase Ready
		stubDeployedInstance(t, nil)

		_, err := hydrateUpgradeState(t.Context(), nil, provider, binding, opts)
		var unfinished *ErrDestroyUnfinished
		if !errors.As(err, &unfinished) {
			t.Fatalf("an upgrade was allowed to start against an instance being torn down: %v", err)
		}
	})

	t.Run("the declaration's phase says so", func(t *testing.T) {
		fakeHome(t) // no marker: this machine did not run the destroy
		inst := atVersion(t, "ghcr.io/devicechain-io", "v1.3.0")
		setPhase(inst, dcv1beta1.PhaseDestroying)
		stubDeclaration(t, inst)
		stubDeployedInstance(t, nil)

		_, err := hydrateUpgradeState(t.Context(), nil, provider, binding, opts)
		if err == nil {
			t.Fatal("an upgrade was allowed to start against a declaration reading Destroying")
		}
		if !strings.Contains(err.Error(), "dcctl destroy") {
			t.Errorf("the refusal does not name the command that finishes the teardown: %v", err)
		}
		if !strings.Contains(err.Error(), "dcctl bootstrap") {
			t.Errorf("the refusal does not say how to build the instance again afterwards: %v", err)
		}
	})

	// 🔴 THE COUNTERWEIGHT. A hydration that refused unconditionally would satisfy both
	// arms above and break every upgrade there is. This one has to get PAST the teardown
	// check and fail for the ordinary reason a stubbed-out cluster fails.
	t.Run("an instance nobody is tearing down is not refused for it", func(t *testing.T) {
		fakeHome(t)
		stubDeclaration(t, atVersion(t, "ghcr.io/devicechain-io", "v1.3.0"))
		stubDeployedInstance(t, nil)

		_, err := hydrateUpgradeState(t.Context(), nil, provider, binding, opts)
		var unfinished *ErrDestroyUnfinished
		if errors.As(err, &unfinished) {
			t.Fatalf("a healthy instance was refused as half-destroyed: %v", err)
		}
		if err == nil || !strings.Contains(err.Error(), "no configuration document") {
			t.Fatalf("the hydration did not reach the read after the teardown check, so this "+
				"control proves nothing: %v", err)
		}
	})
}

// 🔴 THE FUNCTION THAT WOULD DO THE DAMAGE DECLINES TO DO IT, rather than relying on a
// call order two files apart. The refusal above runs before this defer is registered —
// but nothing about finishUpgradePhase itself says so, and a caller that skipped the
// hydration, or a reordering of Upgrade, would reopen it silently. Destroying is the one
// phase another command acts on, so it is the one phase this must not overwrite.
func TestFinishUpgradePhaseWillNotStampOverATeardown(t *testing.T) {
	for _, tc := range []struct {
		name   string
		runErr error
	}{
		{"a successful upgrade would have written Ready", nil},
		{"a failed upgrade would have written Failed", errors.New("the services never rolled over")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inst := atVersion(t, "ghcr.io/devicechain-io", "v1.3.0")
			setPhase(inst, dcv1beta1.PhaseDestroying)
			dyn := declaring(t, inst)

			out := captureOutput(t, func() {
				finishUpgradePhase(t.Context(), dyn, "prod",
					upgradingTo("ghcr.io/devicechain-io", "v1.3.0"), nil, tc.runErr)
			})

			if got := phaseOf(t, dyn); got != dcv1beta1.PhaseDestroying {
				t.Fatalf("an upgrade stamped %q over a teardown that has not finished. The one "+
					"piece of evidence that the cluster holds half an instance is gone, and the "+
					"declaration now says the instance is fine", got)
			}
			// Silence would be worse than the stamp: the operator has just run an upgrade
			// against an instance that is being torn down and needs to know it.
			if !strings.Contains(out, "destroy") {
				t.Errorf("the declined stamp said nothing about the teardown it found:\n%s", out)
			}
		})
	}

	// 🔑 THE COUNTERWEIGHT, AND WITHOUT IT THE GUARD ABOVE IS SATISFIED BY A FUNCTION THAT
	// WRITES NOTHING. Every other phase is still stamped over.
	t.Run("every other phase is still closed out", func(t *testing.T) {
		dyn := declaring(t, upgrading(t))
		finishUpgradePhase(t.Context(), dyn, "prod",
			upgradingTo("ghcr.io/devicechain-io", "v1.3.0"), nil, nil)
		if got := phaseOf(t, dyn); got != dcv1beta1.PhaseReady {
			t.Fatalf("an ordinary upgrade was left reading %q, so the guard above is a function "+
				"that never writes", got)
		}
	})
}

// 🔴 THE READ/WRITE DISTINCTION, WHICH THE MENTION COUNT CANNOT MAKE.
// TestEachVerbNamesItsOwnPhase lists the Phase constants each verb NAMES, and adding a
// guard put PhaseDestroying on finishUpgradePhase's list — so from that test's point of
// view this function may now say the word for any reason at all, including stamping it.
// Here every occurrence of it inside finishUpgradePhase has to be an operand of a
// comparison, which is what "reads it as a guard" means in source.
func TestFinishUpgradePhaseNeverWritesADestroyingPhase(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "upgrade.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}

	var body *ast.FuncDecl
	ast.Inspect(file, func(n ast.Node) bool {
		if fn, ok := n.(*ast.FuncDecl); ok && fn.Name.Name == "finishUpgradePhase" {
			body = fn
		}
		return true
	})
	if body == nil {
		t.Fatal("there is no finishUpgradePhase in upgrade.go, so this check examined nothing")
	}

	// Every position at which PhaseDestroying is an operand of a comparison.
	compared := map[token.Pos]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		bin, ok := n.(*ast.BinaryExpr)
		if !ok {
			return true
		}
		for _, side := range []ast.Expr{bin.X, bin.Y} {
			if sel, ok := side.(*ast.SelectorExpr); ok && sel.Sel.Name == "PhaseDestroying" {
				compared[sel.Sel.Pos()] = true
			}
		}
		return true
	})

	var mentions int
	ast.Inspect(body, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "PhaseDestroying" {
			return true
		}
		mentions++
		if !compared[sel.Sel.Pos()] {
			t.Errorf("finishUpgradePhase uses PhaseDestroying at %s somewhere other than a "+
				"comparison. An upgrade may READ it — that is the guard — but a verb that "+
				"WRITES another verb's word declares the wrong command over a live instance",
				fset.Position(sel.Sel.Pos()))
		}
		return true
	})
	if mentions == 0 {
		t.Fatal("finishUpgradePhase no longer mentions PhaseDestroying at all, so the guard " +
			"that stops an upgrade erasing a half-finished teardown is gone")
	}
}

// 🔴 THE ORDER IS THE GUARANTEE, AND NOTHING ELSE PINS IT. Upgrade registers its terminal
// stamp as a defer; from that line on, EVERY exit writes a phase. The teardown refusal is
// safe only because it is made during the hydration, which happens before that line — an
// ordering this change did not choose and would not notice losing. Asserted on the source
// because Upgrade needs a live cluster to run.
func TestTheTeardownRefusalIsMadeBeforeTheUpgradeCanStampAnyPhase(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "upgrade.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}

	var hydrate, record, stamp token.Pos
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		id, ok := call.Fun.(*ast.Ident)
		if !ok {
			return true
		}
		switch id.Name {
		case "hydrateUpgradeState":
			if hydrate == token.NoPos {
				hydrate = call.Pos()
			}
		case "recordUpgradedVersion":
			if record == token.NoPos {
				record = call.Pos()
			}
		case "finishUpgradePhase":
			if stamp == token.NoPos {
				stamp = call.Pos()
			}
		}
		return true
	})

	for _, c := range []struct {
		what string
		pos  token.Pos
	}{
		{"hydrateUpgradeState", hydrate},
		{"recordUpgradedVersion", record},
		{"finishUpgradePhase", stamp},
	} {
		if c.pos == token.NoPos {
			t.Fatalf("Upgrade no longer calls %s, so this ordering assertion checks nothing", c.what)
		}
	}
	if hydrate > record {
		t.Errorf("the hydration runs at %s, after recordUpgradedVersion at %s — the teardown "+
			"refusal would be made after the declaration had already been rewritten",
			fset.Position(hydrate), fset.Position(record))
	}
	if hydrate > stamp {
		t.Errorf("the hydration runs at %s, after the terminal stamp is registered at %s. From "+
			"that line on every exit writes a phase, so the refusal itself would overwrite the "+
			"Destroying it refused over", fset.Position(hydrate), fset.Position(stamp))
	}

	// And the refusal really is in the hydration, rather than somewhere the ordering above
	// says nothing about.
	state, err := parser.ParseFile(token.NewFileSet(), "upgradestate.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	var refuses bool
	ast.Inspect(state, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "hydrateUpgradeState" {
			return true
		}
		ast.Inspect(fn, func(inner ast.Node) bool {
			call, ok := inner.(*ast.CallExpr)
			if !ok {
				return true
			}
			if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "refuseUnfinishedTeardown" {
				refuses = true
			}
			return true
		})
		return false
	})
	if !refuses {
		t.Fatal("hydrateUpgradeState no longer makes the teardown refusal, so the ordering " +
			"asserted above protects nothing")
	}
}
