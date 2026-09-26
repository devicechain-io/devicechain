// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// These drive the REAL runVerify, through the real userclient session, against a fake
// instance that behaves like one mid-way through an upgrade that replaced the
// signing key: some sign-ins produce a token the data plane refuses with 401, some
// selectTenant calls refuse the identity token login just issued.

// fakeInstance numbers every sign-in: login N issues identity token "id-N", and a
// selectTenant that accepts it issues access token "tok-N".
type fakeInstance struct {
	t   *testing.T
	srv *httptest.Server
	e   entity

	// selectOK and readOK decide, per sign-in number, whether selectTenant accepts
	// that identity token and whether the data plane accepts that access token.
	selectOK func(n int) bool
	readOK   func(n int) bool
	// empty makes an accepted read answer "nothing under that token".
	empty bool

	mu     sync.Mutex
	logins int

	// out is what verify printed: the report an operator reads.
	out bytes.Buffer
}

const probeToken = "rotation-probe"

// plainEntity is the first table entry read by token as a plain list, which is the
// shape most of the table has; the rotation handling is the same for every shape.
func plainEntity(t *testing.T) entity {
	t.Helper()
	for _, e := range allEntities() {
		if e.ReadInput == "" && !e.Single && !e.Publish && e.MatchBy == "" {
			return e
		}
	}
	t.Fatal("the table has no entity read by token as a plain list")
	return entity{}
}

func newFakeInstance(t *testing.T) *fakeInstance {
	t.Helper()
	f := &fakeInstance{
		t:        t,
		e:        plainEntity(t),
		selectOK: func(int) bool { return true },
		readOK:   func(int) bool { return true },
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/user-management/graphql", f.userManagement)
	mux.HandleFunc("/api/"+f.e.Area+"/graphql", f.read)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeInstance) userManagement(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	exp := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	switch {
	case strings.Contains(req.Query, "login("):
		f.mu.Lock()
		f.logins++
		n := f.logins
		f.mu.Unlock()
		_, _ = fmt.Fprintf(w, `{"data":{"login":{"identityToken":"id-%d","expiresAt":%q}}}`, n, exp)
	case strings.Contains(req.Query, "selectTenant("):
		var n int
		_, _ = fmt.Sscanf(fmt.Sprint(req.Variables["identityToken"]), "id-%d", &n)
		if !f.selectOK(n) {
			// What user-management answers when its own key set cannot verify the
			// identity token: a GraphQL error at HTTP 200, not a 401.
			_, _ = fmt.Fprint(w, `{"errors":[{"message":"invalid or expired token"}]}`)
			return
		}
		_, _ = fmt.Fprintf(w, `{"data":{"selectTenant":{"accessToken":"tok-%d","refreshToken":"r-%d","expiresAt":%q}}}`, n, n, exp)
	default:
		http.Error(w, "unexpected operation", http.StatusBadRequest)
	}
}

func (f *fakeInstance) read(w http.ResponseWriter, r *http.Request) {
	var n int
	_, _ = fmt.Sscanf(r.Header.Get("Authorization"), "Bearer tok-%d", &n)
	if !f.readOK(n) {
		// core/graphql's answer to a token its validator cannot verify.
		http.Error(w, "invalid or expired token", http.StatusUnauthorized)
		return
	}
	list := `[{"token":"` + probeToken + `"}]`
	if f.empty {
		list = `[]`
	}
	_, _ = fmt.Fprintf(w, `{"data":{%q:%s}}`, f.e.Read, list)
}

func (f *fakeInstance) signIns() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.logins
}

// verify runs the real subcommand against the fake with a one-row receipt.
func (f *fakeInstance) verify(extra ...string) error {
	f.t.Helper()
	path := filepath.Join(f.t.TempDir(), "receipt.json")
	if err := writeReceipt(path, Receipt{
		Tenant:   "apiprobe",
		Identity: Credential{Email: "probe@apiprobe.invalid", Password: "pw"},
		Written:  time.Now(),
		Entities: []Recorded{{
			Name: f.e.Name, Area: f.e.Area, Token: probeToken,
			Object: json.RawMessage(`{"token":"` + probeToken + `"}`), Fields: f.e.Fields,
		}},
	}); err != nil {
		f.t.Fatal(err)
	}
	argv := append([]string{"--receipt", path, "--server", strings.TrimPrefix(f.srv.URL, "http://"), "--scheme", "http"}, extra...)
	return verifyTo(context.Background(), argv, &f.out)
}

// 🔴 THE MISLABEL. A token the upgraded platform will not accept was reported as
// exitShape — "the schema moved" — when no query had even been evaluated. With no
// window it must fail at once, and say what it is.
func TestARefusedTokenIsDeniedNotAShapeChange(t *testing.T) {
	f := newFakeInstance(t)
	f.readOK = func(int) bool { return false }

	assertCode(t, f.verify(), exitDenied)
	if got := f.signIns(); got != 1 {
		t.Fatalf("signed in %d times with no retry window, want exactly 1", got)
	}
}

// A sign-in the platform refused is the drill failing to authenticate: nothing was
// read, so it is inconclusive — never a verdict about the schema.
func TestASignInThePlatformRefusedIsInconclusiveNotShape(t *testing.T) {
	f := newFakeInstance(t)
	f.selectOK = func(int) bool { return false }

	assertCode(t, f.verify(), exitSetup)
}

// The observed failure: the first sign-in's token is refused (a service still held
// only the old pod's keys), and a later one is accepted. Inside the window, verify
// signs in again and passes.
func TestVerifyRidesOutATokenTheOldPodCannotVerify(t *testing.T) {
	f := newFakeInstance(t)
	f.readOK = func(n int) bool { return n >= 2 }

	if err := f.verify("--auth-window", "3s"); err != nil {
		t.Fatalf("verify did not ride out one refused token inside a 3s window: %v (exit %d)", err, codeOf(err))
	}
	if got := f.signIns(); got != 2 {
		t.Fatalf("signed in %d times, want 2: a retry must sign in AGAIN, because a token the old pod "+
			"issued is refused for good", got)
	}
	// A green run that rode out a rotation must SAY so; read without the retry it
	// would look exactly like a run that never met one.
	if out := f.out.String(); !strings.Contains(out, "  retry   401 from ") ||
		!strings.Contains(out, "1 request(s) were refused while the signing key rotated") {
		t.Fatalf("verify passed after a retry but its report does not say so:\n%s", out)
	}
}

// login and selectTenant are two requests and can land on the two different pods,
// which refuse each other's identity token. That, too, is ridden out.
func TestASignInSplitAcrossPodsIsRiddenOut(t *testing.T) {
	f := newFakeInstance(t)
	f.selectOK = func(n int) bool { return n >= 2 }

	if err := f.verify("--auth-window", "3s"); err != nil {
		t.Fatalf("verify did not ride out one refused sign-in inside a 3s window: %v (exit %d)", err, codeOf(err))
	}
	if got := f.signIns(); got != 2 {
		t.Fatalf("signed in %d times, want 2", got)
	}
}

// The window can FAIL. A tolerance never shown to give up is a skip with extra steps.
// 1.5s with a 1s delay: one retry at +0s, then +1s + 1s is past the budget.
func TestA401PastTheWindowIsDenied(t *testing.T) {
	f := newFakeInstance(t)
	f.readOK = func(int) bool { return false }

	err := f.verify("--auth-window", "1500ms")
	assertCode(t, err, exitDenied)
	if got := f.signIns(); got != 2 {
		t.Fatalf("signed in %d times inside a 1.5s window, want 2", got)
	}
	if msg := err.Error(); !strings.Contains(msg, "after 1") || !strings.Contains(msg, "1 retries") {
		t.Fatalf("the refusal does not say how long and how often it tried: %s", msg)
	}
}

// A sign-in still refused when the window is spent is inconclusive, not DENIED: no
// token was ever presented to the data plane.
func TestASignInThatNeverSettlesIsInconclusive(t *testing.T) {
	f := newFakeInstance(t)
	f.selectOK = func(int) bool { return false }

	assertCode(t, f.verify("--auth-window", "1500ms"), exitSetup)
	if got := f.signIns(); got != 2 {
		t.Fatalf("signed in %d times inside a 1.5s window, want 2", got)
	}
}

// Positive control for the ones above: a platform that refuses nothing is signed in
// to ONCE. A window that re-signed-in regardless would pass every test above for
// the wrong reason.
func TestNothingRefusedMeansOneSignIn(t *testing.T) {
	f := newFakeInstance(t)

	if err := f.verify("--auth-window", "3s"); err != nil {
		t.Fatalf("verify failed against a healthy fake: %v", err)
	}
	if got := f.signIns(); got != 1 {
		t.Fatalf("signed in %d times against a platform that refused nothing, want 1", got)
	}
	if out := f.out.String(); strings.Contains(out, "retry") || strings.Contains(out, "were refused") {
		t.Fatalf("a run that retried nothing reports a retry:\n%s", out)
	}
	if !strings.Contains(f.out.String(), "  ok      "+f.e.Name) {
		t.Fatalf("the report does not carry the row it read back:\n%s", f.out.String())
	}
}

// The deletion control's outcome is untouched by the window: an empty read is
// MISSING at once, and is not retried.
func TestAMissingRowIsStillMissing(t *testing.T) {
	f := newFakeInstance(t)
	f.empty = true

	assertCode(t, f.verify("--auth-window", "3s"), exitMissing)
	if got := f.signIns(); got != 1 {
		t.Fatalf("an empty read signed in %d times, want 1: only a 401 is retried", got)
	}
}

func TestANegativeWindowIsRefused(t *testing.T) {
	f := newFakeInstance(t)
	assertCode(t, f.verify("--auth-window", "-1s"), exitSetup)
	if got := f.signIns(); got != 0 {
		t.Fatalf("a refused flag still signed in %d times", got)
	}
}
