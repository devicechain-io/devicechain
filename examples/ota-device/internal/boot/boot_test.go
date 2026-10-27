// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package boot_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"devicechain.io/examples/ota-device/internal/boot"
	"devicechain.io/examples/ota-device/internal/fsutil"
	"devicechain.io/examples/ota-device/internal/testutil"
)

func TestMain(m *testing.M) {
	code := m.Run()
	testutil.Cleanup()
	os.Exit(code)
}

// The launcher's whole policy as a table: state in, slot to run and state to persist out.
func TestDecide(t *testing.T) {
	cases := []struct {
		name     string
		in       boot.State
		launch   string
		out      boot.State
		rollback bool
	}{
		{"steady", boot.State{Confirmed: "a"}, "a", boot.State{Confirmed: "a"}, false},
		{"steady on b", boot.State{Confirmed: "b"}, "b", boot.State{Confirmed: "b"}, false},
		{"fresh pending gets its one try", boot.State{Confirmed: "a", Pending: "b"}, "b", boot.State{Confirmed: "a", Pending: "b", Tries: 1}, false},
		{"pending that was tried and never confirmed is rolled back", boot.State{Confirmed: "a", Pending: "b", Tries: 1}, "a", boot.State{Confirmed: "a"}, true},
		{"more tries than one still rolls back", boot.State{Confirmed: "b", Pending: "a", Tries: 5}, "b", boot.State{Confirmed: "b"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := boot.Decide(c.in)
			assert.Equal(t, c.launch, d.Launch)
			assert.Equal(t, c.out, d.State)
			assert.Equal(t, c.rollback, d.Rollback != nil)
			if c.rollback {
				assert.Equal(t, c.in.Pending, d.Rollback.From)
				assert.Equal(t, c.in.Confirmed, d.Rollback.To)
			}
		})
	}
}

func TestStateValidation(t *testing.T) {
	for name, s := range map[string]boot.State{
		"no confirmed":            {},
		"bad confirmed":           {Confirmed: "c"},
		"bad pending":             {Confirmed: "a", Pending: "z"},
		"tries without a pending": {Confirmed: "a", Tries: 1},
		"negative tries":          {Confirmed: "a", Pending: "b", Tries: -1},
	} {
		assert.Error(t, s.Validate(), name)
		assert.Error(t, boot.Save(t.TempDir(), s), name, "an invalid state is never written")
	}
}

// A corrupt boot.json must not make the launcher guess: it boots the confirmed slot as of the last
// quiescent state, and never the slot that was only pending.
func TestCorruptStateBootsTheConfirmedSlot(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, boot.Save(root, boot.State{Confirmed: "a"}))
	require.NoError(t, boot.Stage(root, "b")) // boot.json now says pending b; the good copy says confirmed a

	for name, content := range map[string]string{
		"garbage":       "{not json",
		"empty":         "",
		"wrong slot":    `{"confirmed":"x"}`,
		"truncated":     `{"confirmed":"b","pend`,
		"pending alone": `{"pending":"b"}`,
	} {
		require.NoError(t, os.WriteFile(boot.StatePath(root), []byte(content), 0o600), name)
		s, recovered, err := boot.Load(root)
		require.NoError(t, err, name)
		assert.True(t, recovered, name)
		assert.Equal(t, boot.State{Confirmed: "a"}, s, name)
		assert.Equal(t, "a", boot.Decide(s).Launch, name)
	}

	// With no good copy either, the launcher refuses rather than guess.
	require.NoError(t, os.Remove(filepath.Join(root, "boot.json.good")))
	_, _, err := boot.Load(root)
	assert.Error(t, err)
}

func TestConfirmOnlyPromotesTheSlotThatIsPending(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, boot.Save(root, boot.State{Confirmed: "a"}))
	require.NoError(t, boot.Stage(root, "b"))

	changed, err := boot.Confirm(root, "a") // the old slot cannot confirm the new one's boot
	require.NoError(t, err)
	assert.False(t, changed)
	s, _, _ := boot.Load(root)
	assert.Equal(t, boot.State{Confirmed: "a", Pending: "b"}, s)

	changed, err = boot.Confirm(root, "b")
	require.NoError(t, err)
	assert.True(t, changed)
	s, _, _ = boot.Load(root)
	assert.Equal(t, boot.State{Confirmed: "b"}, s)
}

func TestInitRefusesToGuess(t *testing.T) {
	root := t.TempDir()
	assert.Error(t, boot.Init(root), "no image in slot a")
	require.NoError(t, os.MkdirAll(boot.SlotDir(root, "a"), 0o700))
	require.NoError(t, os.WriteFile(boot.ImagePath(root, "a"), []byte("x"), 0o700))
	require.NoError(t, boot.Init(root))
	s, _, _ := boot.Load(root)
	assert.Equal(t, boot.State{Confirmed: "a"}, s)
	// an unreadable boot.json with no good copy is not silently re-initialised
	require.NoError(t, os.Remove(filepath.Join(root, "boot.json.good")))
	require.NoError(t, os.WriteFile(boot.StatePath(root), []byte("{"), 0o600))
	assert.Error(t, boot.Init(root))
}

type step struct {
	code    int
	confirm bool // the fake child confirms itself before exiting
}

// fakeRunner plays a scripted child so the supervisor's decisions can be observed without a process.
func fakeRunner(root string, steps []step, ran *[]string) boot.Runner {
	i := 0
	return func(ctx context.Context, slot string, _ func() bool) (int, error) {
		*ran = append(*ran, slot)
		s := steps[i]
		i++
		if s.confirm {
			_, _ = boot.Confirm(root, slot)
		}
		return s.code, nil
	}
}

func newRoot(t *testing.T, confirmed string) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, boot.Save(root, boot.State{Confirmed: confirmed}))
	return root
}

func loop(t *testing.T, root string, r boot.Runner, n int) boot.Result {
	t.Helper()
	sup := &boot.Supervisor{Root: root, Run: r, MinBackoff: time.Millisecond, MaxBackoff: time.Millisecond,
		Sleep: func(context.Context, time.Duration) {}}
	res, err := sup.Loop(context.Background(), n)
	require.NoError(t, err)
	return res
}

func TestSupervisorPromotesAHealthyNewSlot(t *testing.T) {
	root := newRoot(t, "a")
	require.NoError(t, boot.Stage(root, "b"))
	var ran []string
	res := loop(t, root, fakeRunner(root, []step{{code: 0, confirm: true}}, &ran), 5)
	assert.Equal(t, []string{"b"}, res.Launches)
	s, _, _ := boot.Load(root)
	assert.Equal(t, boot.State{Confirmed: "b"}, s)
	rb, err := boot.ReadRollback(root)
	require.NoError(t, err)
	assert.Nil(t, rb, "a healthy boot leaves no rollback note")
}

func TestSupervisorRollsBackACrashingNewSlot(t *testing.T) {
	root := newRoot(t, "a")
	require.NoError(t, boot.Stage(root, "b"))
	var ran []string
	// b crashes without confirming; the launcher must then run a (which exits 0 and ends the loop).
	res := loop(t, root, fakeRunner(root, []step{{code: 1}, {code: 0}}, &ran), 5)
	assert.Equal(t, []string{"b", "a"}, res.Launches, "the new slot gets exactly one try")
	s, _, _ := boot.Load(root)
	assert.Equal(t, boot.State{Confirmed: "a"}, s, "b was never confirmed")
	rb, err := boot.ReadRollback(root)
	require.NoError(t, err)
	require.NotNil(t, rb)
	assert.Equal(t, "b", rb.From)
	assert.Equal(t, "a", rb.To)
}

func TestSupervisorRestartsOnRebootRequestAndBacksOffOnCrashes(t *testing.T) {
	root := newRoot(t, "a")
	var ran []string
	res := loop(t, root, fakeRunner(root, []step{{code: boot.ExitReboot}, {code: 1}, {code: 1}, {code: 0}}, &ran), 10)
	assert.Equal(t, []string{"a", "a", "a", "a"}, res.Launches)
}

// The same decisions with real processes, so they hold on Windows too: a real image is started in
// the pending slot, and a hang is killed by the confirm watchdog.
func TestProcessRunnerRealImages(t *testing.T) {
	good := testutil.Fake(t, "1.0.1", "ota-device-example", "good")
	crash := testutil.Fake(t, "1.0.2", "ota-device-example", "crash")
	hang := testutil.Fake(t, "1.0.3", "ota-device-example", "hang")
	base := testutil.Fake(t, "1.0.0", "ota-device-example", "good")

	install := func(t *testing.T, root, slot, src string) {
		t.Helper()
		require.NoError(t, os.MkdirAll(boot.SlotDir(root, slot), 0o700))
		b, err := os.ReadFile(src)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(boot.ImagePath(root, slot), b, 0o700))
	}
	run := func(root string, n int, confirmTimeout time.Duration) boot.Result {
		sup := &boot.Supervisor{Root: root, Run: boot.ProcessRunner(root, nil, confirmTimeout, nil),
			MinBackoff: time.Millisecond, MaxBackoff: time.Millisecond}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		res, err := sup.Loop(ctx, n)
		require.NoError(t, err)
		return res
	}

	t.Run("good image confirms", func(t *testing.T) {
		root := t.TempDir()
		install(t, root, "a", base)
		install(t, root, "b", good)
		require.NoError(t, boot.Save(root, boot.State{Confirmed: "a"}))
		require.NoError(t, boot.Stage(root, "b"))
		res := run(root, 3, time.Minute)
		assert.Equal(t, []string{"b"}, res.Launches)
		s, _, _ := boot.Load(root)
		assert.Equal(t, boot.State{Confirmed: "b"}, s)
	})
	t.Run("crashing image rolls back", func(t *testing.T) {
		root := t.TempDir()
		install(t, root, "a", base)
		install(t, root, "b", crash)
		require.NoError(t, boot.Save(root, boot.State{Confirmed: "a"}))
		require.NoError(t, boot.Stage(root, "b"))
		res := run(root, 2, time.Minute)
		assert.Equal(t, []string{"b", "a"}, res.Launches)
		s, _, _ := boot.Load(root)
		assert.Equal(t, boot.State{Confirmed: "a"}, s)
		rb, _ := boot.ReadRollback(root)
		require.NotNil(t, rb)
		assert.Equal(t, "b", rb.From)
	})
	t.Run("hanging image is killed by the watchdog and rolled back", func(t *testing.T) {
		root := t.TempDir()
		install(t, root, "a", base)
		install(t, root, "b", hang)
		require.NoError(t, boot.Save(root, boot.State{Confirmed: "a"}))
		require.NoError(t, boot.Stage(root, "b"))
		res := run(root, 2, 500*time.Millisecond)
		assert.Equal(t, []string{"b", "a"}, res.Launches)
		s, _, _ := boot.Load(root)
		assert.Equal(t, boot.State{Confirmed: "a"}, s)
	})
	t.Run("an image can be replaced over a slot that is not running", func(t *testing.T) {
		root := t.TempDir()
		install(t, root, "a", base)
		install(t, root, "b", crash) // a previous, already-rolled-back image sits in b
		part := boot.ImagePath(root, "b") + ".new"
		b, err := os.ReadFile(good)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(part, b, 0o700))
		_ = os.Remove(boot.ImagePath(root, "b"))
		require.NoError(t, os.Rename(part, boot.ImagePath(root, "b")))
		require.NoError(t, fsutil.SyncDir(boot.SlotDir(root, "b")))
		require.NoError(t, boot.Save(root, boot.State{Confirmed: "a"}))
		require.NoError(t, boot.Stage(root, "b"))
		res := run(root, 2, time.Minute)
		assert.Equal(t, []string{"b"}, res.Launches)
		s, _, _ := boot.Load(root)
		assert.Equal(t, boot.State{Confirmed: "b"}, s, "the replacement image, not the old one, ran")
	})
}
