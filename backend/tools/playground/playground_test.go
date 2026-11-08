// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func cfg3() Config {
	return Config{Scenarios: []string{"sitepulse", "buildingpulse"}, Slots: 3, ResetHour: 3, Seed: 1}
}

func at(day int, hour int) time.Time {
	return time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, day).Add(time.Duration(hour) * time.Hour)
}

// fakePlatform is the part of an instance the rotation depends on: a sim is a tenant with
// state, a destroyed tenant's token is held for 12 hours (the platform's rule), and a
// create at a held token is refused. It is driven through the same Runner seam the real
// dcctl is, so the Driver under test cannot tell the difference.
type fakePlatform struct {
	mu      sync.Mutex
	now     time.Time
	tenants map[string]map[string]bool // sim name -> state keys ("seed" is the seeded state)
	heldTil map[string]time.Time
	log     []string
	failOn  map[string]bool // command line prefix -> fail
	runs    map[string]int  // sim name -> times `sim run` was started
	runDies bool            // `sim run` exits at once instead of running until cancelled
	// lostRecord names sims whose tenant exists but whose local dcctl record is gone: dcctl
	// created the tenant and died before saving the record, or the record volume was wiped.
	lostRecord map[string]bool
	probes     []string // tenant-deletion status command lines, as run
}

func newPlatform() *fakePlatform {
	return &fakePlatform{tenants: map[string]map[string]bool{}, heldTil: map[string]time.Time{}, failOn: map[string]bool{}}
}

func (p *fakePlatform) Run(ctx context.Context, name string, args ...string) (string, error) {
	if len(args) >= 3 && args[0] == "sim" && args[1] == "run" {
		p.mu.Lock()
		if p.runs == nil {
			p.runs = map[string]int{}
		}
		p.runs[args[2]]++
		dies := p.runDies
		p.mu.Unlock()
		if dies {
			return "simulator crashed", errors.New("exit 2")
		}
		<-ctx.Done()
		return "", ctx.Err()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	line := strings.Join(args, " ")
	p.log = append(p.log, line)
	for prefix := range p.failOn {
		if strings.HasPrefix(line, prefix) {
			return "boom", errors.New("exit 1")
		}
	}
	switch {
	case strings.HasPrefix(line, "sim create "):
		sim := args[2]
		if until, held := p.heldTil[sim]; held && p.now.Before(until) {
			return "tenant token is still held", errors.New("exit 1")
		}
		if _, ok := p.tenants[sim]; !ok {
			p.tenants[sim] = map[string]bool{"seed": true}
		}
		return "", nil
	case strings.HasPrefix(line, "tenant-deletion status "):
		p.probes = append(p.probes, line)
		sim := strings.TrimPrefix(args[2], "sim-")
		if !strings.Contains(line, "--password ") {
			return "Error: --email and --password are required", errors.New("exit 1")
		}
		if _, ok := p.tenants[sim]; ok {
			return "Error: the tenant is active and is not being deleted: tenant \"" + args[2] + "\" is \"active\"", errors.New("exit 1")
		}
		if _, held := p.heldTil[sim]; held {
			return "erased", nil
		}
		return "Error: no deletion record for tenant \"" + args[2] + "\": it was never deleted", errors.New("exit 1")
	case strings.HasPrefix(line, "sim destroy "):
		sim := args[2]
		if p.lostRecord[sim] {
			return "Error: no sim named " + sim + " (create it with dcctl sim create)", errors.New("exit 1")
		}
		// dcctl sim destroy has a smaller flag set than sim create, and cobra refuses the
		// rest as unknown before it looks at the sim at all. The fake does the same.
		for _, a := range args[3:] {
			if strings.HasPrefix(a, "--") && !destroyFlags[a] {
				return "Error: unknown flag: " + a, errors.New("exit 1")
			}
		}
		if _, ok := p.tenants[sim]; !ok {
			return "no sim named " + sim, errors.New("exit 1")
		}
		delete(p.tenants, sim)
		p.heldTil[sim] = p.now.Add(12 * time.Hour)
		return "", nil
	}
	return "", fmt.Errorf("unexpected command %q", line)
}

// destroyFlags are the flags `dcctl sim destroy` accepts that the driver passes: its own
// --host-header and the sim group's persistent ones. TestDcctlSourceStillMatches reads
// them out of dcctl's source, so this list cannot drift from the real command.
var destroyFlags = map[string]bool{"--server": true, "--instance": true, "--host-header": true, "--tls": true}

func (p *fakePlatform) driver(c Config) *Driver {
	return &Driver{Cfg: c, Dcctl: "dcctl", Simulator: "dc-simulator", BaseControlPort: 8090, Runner: p,
		ProbeArgs: []string{"--server", "dc.example", "--email", "su@example"}, AdminPassword: "pw"}
}

func TestSlotChangesExactlyAtTheResetHour(t *testing.T) {
	c := cfg3()
	before := time.Date(2026, 11, 8, 2, 59, 59, 0, time.UTC)
	after := time.Date(2026, 11, 8, 3, 0, 0, 0, time.UTC)
	if Slot(before, c) == Slot(after, c) {
		t.Fatalf("slot did not change at the reset hour")
	}
	if Slot(after, c) != Slot(after.Add(20*time.Hour), c) {
		t.Fatalf("slot changed inside a day")
	}
}

func TestSlotIsTimeZoneIndependent(t *testing.T) {
	c := cfg3()
	utc := time.Date(2026, 11, 8, 2, 30, 0, 0, time.UTC)
	tokyo := utc.In(time.FixedZone("x", 9*3600))
	if Slot(utc, c) != Slot(tokyo, c) {
		t.Fatalf("the same instant is in two slots")
	}
}

func TestEverySlotIsVisitedAndNeverTwiceRunning(t *testing.T) {
	c := cfg3()
	seen := map[int]bool{}
	for d := 0; d < c.Slots; d++ {
		seen[Slot(at(d, 12), c)] = true
	}
	if len(seen) != c.Slots {
		t.Fatalf("visited %d of %d slots", len(seen), c.Slots)
	}
}

func TestPlanCreatesTheCurrentSlotAndDestroysTheRest(t *testing.T) {
	c := cfg3()
	p := PlanRotation(at(4, 12), c)
	if len(p.Create) != len(c.Scenarios) || len(p.Destroy) != len(c.Scenarios)*(c.Slots-1) {
		t.Fatalf("plan sizes: %+v", p)
	}
	for _, d := range p.Destroy {
		for _, cr := range p.Create {
			if d == cr {
				t.Fatalf("%s is both created and destroyed", d)
			}
		}
	}
	for _, n := range append(p.Create, p.Destroy...) {
		if !ValidSimName(n) {
			t.Fatalf("invalid sim name %q", n)
		}
	}
}

func TestValidate(t *testing.T) {
	bad := map[string]Config{
		"one slot":       {Scenarios: []string{"sitepulse"}, Slots: 1, ResetHour: 3},
		"two slots":      {Scenarios: []string{"sitepulse"}, Slots: 2, ResetHour: 3},
		"too many slots": {Scenarios: []string{"sitepulse"}, Slots: MaxSlots + 1, ResetHour: 3},
		"hour 24":        {Scenarios: []string{"sitepulse"}, Slots: 3, ResetHour: 24},
		"hour -1":        {Scenarios: []string{"sitepulse"}, Slots: 3, ResetHour: -1},
		"no scenarios":   {Slots: 3, ResetHour: 3},
		"towerpulse":     {Scenarios: []string{"towerpulse"}, Slots: 3, ResetHour: 3},
		"unknown":        {Scenarios: []string{"coldline"}, Slots: 3, ResetHour: 3},
		"duplicate":      {Scenarios: []string{"sitepulse", "sitepulse"}, Slots: 3, ResetHour: 3},
	}
	for name, c := range bad {
		if err := c.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := cfg3().Validate(); err != nil {
		t.Fatalf("the good config was refused: %v", err)
	}
}

// The negative control. A reset that deletes the tenants and recreates them under the
// same names is the obvious design, and the platform refuses it for 12 hours. This test
// is what shows the fake platform enforces that, so the rotation test below is passing
// against a platform that CAN refuse.
func TestNaiveSameNameResetIsRefused(t *testing.T) {
	p := newPlatform()
	p.now = at(0, 3)
	if _, err := p.Run(context.Background(), "dcctl", "sim", "create", "pg-sitepulse-a"); err != nil {
		t.Fatal(err)
	}
	p.tenants["pg-sitepulse-a"]["visitor-mutation"] = true
	p.now = at(1, 3)
	if _, err := p.Run(context.Background(), "dcctl", "sim", "destroy", "pg-sitepulse-a"); err != nil {
		t.Fatal(err)
	}
	p.now = at(1, 3).Add(time.Minute)
	if _, err := p.Run(context.Background(), "dcctl", "sim", "create", "pg-sitepulse-a"); err == nil {
		t.Fatal("the platform allowed a tenant to be recreated inside the token hold")
	}
}

func TestNightlyRotationRestoresTheSeedAndDropsAPlantedMutation(t *testing.T) {
	p := newPlatform()
	d := p.driver(cfg3())
	var prev string
	var logbuf strings.Builder
	d.Log = &logbuf
	for day := 0; day < 12; day++ {
		p.now = at(day, 3).Add(5 * time.Minute) // the CronJob fires five minutes after the boundary
		plan, err := d.Rotate(context.Background(), p.now)
		if err != nil {
			t.Fatalf("day %d: %v", day, err)
		}
		cur := plan.Create[0]
		if got := p.tenants[cur]; len(got) != 1 || !got["seed"] {
			t.Fatalf("day %d: %s is not exactly the seed state: %v", day, cur, got)
		}
		if prev != "" {
			if _, still := p.tenants[prev]; still {
				t.Fatalf("day %d: yesterday's tenant %s survived the reset", day, prev)
			}
		}
		if len(p.tenants) != len(d.Cfg.Scenarios) {
			t.Fatalf("day %d: %d tenants live, want %d", day, len(p.tenants), len(d.Cfg.Scenarios))
		}
		// A visitor mutates the live tenants during the day.
		for name := range p.tenants {
			p.tenants[name]["visitor-mutation"] = true
		}
		prev = cur
	}
	if strings.Contains(logbuf.String(), "could not be destroyed") || strings.Contains(logbuf.String(), "failed") {
		t.Fatalf("a rotation that needed no repair reported a failure:\n%s", logbuf.String())
	}
}

// lockedBuilder is a log sink the supervising goroutines and the test share.
type lockedBuilder struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuilder) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuilder) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

// A clock the test moves.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time  { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) set(t time.Time) { c.mu.Lock(); c.t = t; c.mu.Unlock() }

func TestRunKeepsOneSimulatorPerScenarioAndStopsOnCancel(t *testing.T) {
	p := newPlatform()
	d := p.driver(cfg3())
	d.CheckEvery = 5 * time.Millisecond
	clk := &clock{t: at(1, 12)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx, clk.now) }()
	waitFor(t, func() bool { p.mu.Lock(); defer p.mu.Unlock(); return len(p.runs) == 2 })
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("a cancelled run is not an error: %v", err)
	}
	for name, n := range p.runs {
		if n != 1 {
			t.Errorf("%s was started %d times", name, n)
		}
	}
}

func TestRunExitsWhenTheDayEndsSoTheNextStartRotates(t *testing.T) {
	p := newPlatform()
	d := p.driver(cfg3())
	d.CheckEvery = 5 * time.Millisecond
	clk := &clock{t: at(1, 12)}
	done := make(chan error, 1)
	go func() { done <- d.Run(context.Background(), clk.now) }()
	waitFor(t, func() bool { p.mu.Lock(); defer p.mu.Unlock(); return len(p.runs) == 2 })
	select {
	case err := <-done:
		t.Fatalf("exited inside the day: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	clk.set(at(2, 3).Add(time.Minute))
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("the day ended and Run reported success: the pod would not be restarted")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not notice the day had ended")
	}
}

func TestADeadSimulatorIsRestarted(t *testing.T) {
	p := newPlatform()
	p.runDies = true
	d := p.driver(Config{Scenarios: []string{"sitepulse"}, Slots: 3, ResetHour: 3, Seed: 1})
	d.RestartPause = time.Millisecond
	d.CheckEvery = time.Hour
	var logbuf lockedBuilder
	d.Log = &logbuf
	clk := &clock{t: at(1, 12)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx, clk.now) }()
	waitFor(t, func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		for _, n := range p.runs {
			if n >= 3 {
				return true
			}
		}
		return false
	})
	cancel()
	<-done
	// The pause grows between restarts; a simulator that dies at once must not be
	// restarted in a tight loop.
	if !strings.Contains(logbuf.String(), "restarting in 2ms") || !strings.Contains(logbuf.String(), "restarting in 4ms") {
		t.Fatalf("the restart pause did not grow:\n%s", logbuf.String())
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestACrashRestartInsideADayKeepsTheDay(t *testing.T) {
	p := newPlatform()
	d := p.driver(cfg3())
	p.now = at(2, 3).Add(5 * time.Minute)
	plan, _ := d.Rotate(context.Background(), p.now)
	p.tenants[plan.Create[0]]["live-state"] = true
	p.now = at(2, 15)
	if _, err := d.Rotate(context.Background(), p.now); err != nil {
		t.Fatal(err)
	}
	if !p.tenants[plan.Create[0]]["live-state"] {
		t.Fatal("a restart inside the same day wiped the day's state")
	}
}

func TestCreateFailureIsFatalAndNothingIsDestroyed(t *testing.T) {
	p := newPlatform()
	p.failOn["sim create "] = true
	d := p.driver(cfg3())
	if _, err := d.Rotate(context.Background(), at(1, 12)); err == nil {
		t.Fatal("a failed create was swallowed")
	}
	for _, l := range p.log {
		if strings.HasPrefix(l, "sim destroy") {
			t.Fatalf("destroyed %q after the create failed", l)
		}
	}
}

func TestDestroyFailureIsNotFatal(t *testing.T) {
	p := newPlatform()
	p.tenants["pg-sitepulse-b"] = map[string]bool{"seed": true}
	p.failOn["sim destroy pg-sitepulse-b"] = true
	var buf strings.Builder
	d := p.driver(cfg3())
	d.Log = &buf
	if _, err := d.Rotate(context.Background(), at(0, 12)); err != nil {
		t.Fatalf("a failed destroy stopped the rotation: %v", err)
	}
	if !strings.Contains(buf.String(), "pg-sitepulse-b") {
		t.Fatalf("the stale sim was not reported: %q", buf.String())
	}
}

func TestCreatesPrecedeDestroys(t *testing.T) {
	p := newPlatform()
	d := p.driver(cfg3())
	if _, err := d.Rotate(context.Background(), at(1, 12)); err != nil {
		t.Fatal(err)
	}
	lastCreate, firstDestroy := -1, len(p.log)
	for i, l := range p.log {
		if strings.HasPrefix(l, "sim create") {
			lastCreate = i
		}
		if strings.HasPrefix(l, "sim destroy") && i < firstDestroy {
			firstDestroy = i
		}
	}
	if lastCreate > firstDestroy {
		t.Fatalf("a destroy ran before the last create: %v", p.log)
	}
}

func TestCreateCarriesTheSeedAndDeploymentArgs(t *testing.T) {
	p := newPlatform()
	d := p.driver(cfg3())
	d.Cfg.Seed = 7
	d.ReachArgs = []string{"--server", "x.example", "--instance", "pg"}
	d.CreateArgs = []string{"--ingress", "http://ingress.example:8081"}
	if _, err := d.Rotate(context.Background(), at(1, 12)); err != nil {
		t.Fatal(err)
	}
	var create, destroy string
	for _, l := range p.log {
		if strings.HasPrefix(l, "sim create pg-buildingpulse") {
			create = l
		}
		if destroy == "" && strings.HasPrefix(l, "sim destroy") {
			destroy = l
		}
	}
	for _, want := range []string{"--manifest buildingpulse", "--seed 7", "--control-addr http://localhost:8091", "--server x.example", "--instance pg", "--ingress http://ingress.example:8081"} {
		if !strings.Contains(create, want) {
			t.Errorf("create %q lacks %q", create, want)
		}
	}
	if !strings.HasSuffix(destroy, "--server x.example --instance pg") {
		t.Errorf("destroy %q does not reach the same instance", destroy)
	}
	if strings.Contains(destroy, "--ingress") {
		t.Errorf("destroy %q carries a create-only flag, which dcctl refuses as unknown", destroy)
	}
}

func TestRestartPatchesTheDeploymentTemplate(t *testing.T) {
	var gotPath, gotAuth, gotCT, gotBody, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth, gotCT, gotMethod = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Content-Type"), r.Method
		b := make([]byte, 512)
		n, _ := r.Body.Read(b)
		gotBody = string(b[:n])
		w.WriteHeader(200)
	}))
	defer srv.Close()
	r := Restarter{APIBase: srv.URL, Token: "tok", Namespace: "ns", Deployment: "playground-driver"}
	if err := r.Restart(context.Background(), at(3, 3)); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPatch || gotPath != "/apis/apps/v1/namespaces/ns/deployments/playground-driver" {
		t.Errorf("wrong request: %s %s", gotMethod, gotPath)
	}
	if gotAuth != "Bearer tok" || gotCT != "application/strategic-merge-patch+json" {
		t.Errorf("wrong headers: %q %q", gotAuth, gotCT)
	}
	if !strings.Contains(gotBody, `"spec":{"template":{"metadata":{"annotations":{"`+resetAnnotation+`":"2026-11-04T03:00:00Z"`) {
		t.Errorf("wrong body: %s", gotBody)
	}
}

func TestRestartFailsWhenTheAPIServerRefuses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer srv.Close()
	r := Restarter{APIBase: srv.URL, Token: "t", Namespace: "n", Deployment: "d"}
	if err := r.Restart(context.Background(), time.Now()); err == nil {
		t.Fatal("a 403 was reported as a restart")
	}
	if err := (Restarter{}).Restart(context.Background(), time.Now()); err == nil {
		t.Fatal("an empty restarter did nothing and said so")
	}
}

// The driver leans on three facts about dcctl that nothing else would notice moving.
func TestDcctlSourceStillMatchesWhatTheDriverAssumes(t *testing.T) {
	root := repoRoot(t)
	rec, err := os.ReadFile(filepath.Join(root, "backend", "cli", "sim", "record.go"))
	if err != nil {
		t.Fatal(err)
	}
	del, err := os.ReadFile(filepath.Join(root, "backend", "cli", "deletions", "deletions.go"))
	if err != nil {
		t.Fatal(err)
	}
	cmdSrc, err := os.ReadFile(filepath.Join(root, "backend", "cli", "cmd", "tenant_deletion.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(del), `"`+tenantActive+`"`) {
		t.Errorf("dcctl no longer says %q of a live tenant; the driver would read a live tenant as gone or unknowable", tenantActive)
	}
	if !strings.Contains(string(cmdSrc), `"`+noDeletionRecord+` %q`) {
		t.Errorf("dcctl no longer says %q; the driver would never confirm a tenant gone", noDeletionRecord)
	}
	if !strings.Contains(string(rec), `"`+noSuchSim+` %q`) {
		t.Errorf("dcctl no longer answers a missing sim with %q; the driver would fail every first night", noSuchSim)
	}
	m := regexp.MustCompile(`KnownManifestIds = \[\]string\{([^}]*)\}`).FindStringSubmatch(string(rec))
	if m == nil {
		t.Fatal("cannot find KnownManifestIds")
	}
	for _, a := range AllowedScenarios {
		if !strings.Contains(m[1], `"`+a+`"`) {
			t.Errorf("allowed scenario %q is not a dcctl manifest", a)
		}
	}
	create, err := os.ReadFile(filepath.Join(root, "backend", "cli", "cmd", "sim_create.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{`"seed"`, `"manifest"`, `"tenant-name"`, `"control-addr"`} {
		if !strings.Contains(string(create), flag) && !strings.Contains(readSim(t, root), flag) {
			t.Errorf("dcctl sim create has no %s flag", flag)
		}
	}
	// The flags destroy accepts are its own plus the sim group's persistent ones; the
	// driver's reach flags must all be among them, and the create-only flags must not be.
	destroy, err := os.ReadFile(filepath.Join(root, "backend", "cli", "cmd", "sim_destroy.go"))
	if err != nil {
		t.Fatal(err)
	}
	for flag := range destroyFlags {
		name := `"` + strings.TrimPrefix(flag, "--") + `"`
		if !strings.Contains(string(destroy), name) && !strings.Contains(readSim(t, root), name) {
			t.Errorf("dcctl sim destroy no longer has %s: the driver would be refused on every night", flag)
		}
	}
	for _, flag := range []string{`"ingress"`, `"mqtt-broker"`} {
		if strings.Contains(string(destroy), flag) {
			t.Errorf("dcctl sim destroy now has %s: create-only flags may be passed to it, update ReachArgs", flag)
		}
	}
	run, err := os.ReadFile(filepath.Join(root, "backend", "cli", "cmd", "sim_run.go"))
	if err != nil || !strings.Contains(string(run), `"simulator"`) {
		t.Errorf("dcctl sim run has no --simulator flag")
	}
}

func readSim(t *testing.T, root string) string {
	b, err := os.ReadFile(filepath.Join(root, "backend", "cli", "cmd", "sim.go"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.work")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("no go.work above the test")
	return ""
}

// The OpenTofu root validates the scenario list on its own, because a bad one should
// fail the plan and not the pod. The two lists are the same list.
func TestOpenTofuAllowsTheSameScenariosAsTheDriver(t *testing.T) {
	root := repoRoot(t)
	b, err := os.ReadFile(filepath.Join(root, "deploy", "playground", "variables.tf"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`contains\(\[([^\]]*)\], s\)`).FindStringSubmatch(string(b))
	if m == nil {
		t.Fatal("cannot find the scenario allow-list in variables.tf")
	}
	var tf []string
	for _, p := range strings.Split(m[1], ",") {
		tf = append(tf, strings.Trim(strings.TrimSpace(p), `"`))
	}
	if strings.Join(sortedCopy(tf), ",") != strings.Join(sortedCopy(AllowedScenarios), ",") {
		t.Fatalf("deploy/playground/variables.tf allows %v, the driver allows %v", tf, AllowedScenarios)
	}
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
