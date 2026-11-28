// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"bytes"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// TestInstallOnAConPTY is the Windows console smoke test: dcctl run as a console program
// on a pseudo console (ConPTY), the console a Windows terminal gives a program it hosts,
// through the presenter selection paths. For each, a probe process attached to the same
// console reads the console's input and output modes before dcctl starts and after it
// exits, and they must be the same: a program that leaves virtual-terminal input on, or
// line input off, breaks the shell it returns to.
//
// 🔴 THE TERMINAL-UI CASE CARRIES ITS OWN NEGATIVE CONTROL. "The modes are the same after"
// is also what a run that never took the console reports, so that case also probes the
// console WHILE the terminal UI holds it, and requires line input to be off then. A run
// that fell back to plain fails the test instead of passing it for nothing.
//
// 🔴 A HOLDER PROCESS STAYS ON THE CONSOLE FOR THE WHOLE CASE, as the shell that started
// dcctl does. A console with no client left forgets the modes its last client set, so
// without one a program that leaves line input off passes this test. That was measured,
// not assumed: a child that cleared line input and exited passed every case until the
// holder was added, and fails every case with it.
//
// The child is this test binary, so nothing is built and nothing outside the repository
// is needed. Its PATH holds only the system directory: no docker, kind, kubectl or tofu
// can be found, so nothing here can reach a cluster.
func TestInstallOnAConPTY(t *testing.T) {
	if os.Getenv(conptyChildEnv) != "" {
		return
	}
	for _, tc := range []struct {
		name string
		args []string
		// tui: the terminal UI takes the console, and keys are typed once it has.
		tui  bool
		keys string
		// ready is text the terminal UI must have DRAWN before the keys are typed. Taking
		// the console is not drawing a screen: line input goes off before the first frame
		// reaches the console, so a key typed in between is read before any screen is on
		// it, and a Ctrl+C there cancels from a screen that was never drawn.
		ready string
		// exit is the status dcctl must exit with. The dry run rehearses as plain output,
		// and its preflight fails here for want of the tools.
		exit uint32
		// shows is text the run must have drawn.
		shows string
	}{
		{name: "help", args: []string{"install", "local", "--ui=tui", "--help"}, exit: 0, shows: "--no-splash"},
		{name: "plain-help", args: []string{"install", "local", "--ui=plain", "--help"}, exit: 0, shows: "--no-splash"},
		{name: "dry-run", args: []string{"install", "local", "--ui=tui", "--dry-run", "--version", "v1.12.0"}, exit: 1,
			shows: "Preflight:"},
		{name: "tui-ctrl-c", args: []string{"install", "local", "--ui=tui", "--no-splash", "--version", "v1.12.0"}, tui: true, keys: "\x03",
			ready: "No changes have been made", exit: 1, shows: "No changes have been made"},
		{name: "tui-ctrl-c-splash", args: []string{"install", "local", "--ui=tui", "--version", "v1.12.0"}, tui: true, keys: "\x03", exit: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pc := newConPTY(t, 120, 40)
			env := conptyEnv(t)
			pc.start(t, env, "hold")
			before := pc.probe(t, env)

			proc := pc.start(t, env, "dcctl", tc.args...)
			if tc.tui {
				during := pc.awaitRawInput(t, env, before, proc)
				if during.in == before.in {
					t.Fatalf("the terminal UI never took the console (input mode %#x throughout)", before.in)
				}
				if tc.ready != "" {
					pc.awaitText(t, tc.ready, proc)
				}
				pc.type_(t, tc.keys)
			}
			code := pc.wait(t, proc, 60*time.Second)
			after := pc.probe(t, env)

			if after != before {
				t.Errorf("dcctl %v left the console's modes changed: input %#x -> %#x, output %#x -> %#x\nscreen:\n%s",
					tc.args, before.in, after.in, before.out, after.out, pc.text())
			}
			if code != tc.exit {
				t.Errorf("dcctl %v exited %d, want %d\nscreen:\n%s", tc.args, code, tc.exit, pc.text())
			}
			if tc.shows != "" && !strings.Contains(pc.text(), tc.shows) {
				t.Errorf("dcctl %v never drew %q; screen:\n%s", tc.args, tc.shows, pc.text())
			}
		})
	}
}

const (
	conptyChildEnv = "DCCTL_CONPTY_CHILD"
	conptyOutEnv   = "DCCTL_CONPTY_OUT"
)

// TestConPTYChild is the child half: a console-mode probe, or dcctl with the arguments
// the parent passed.
func TestConPTYChild(t *testing.T) {
	switch os.Getenv(conptyChildEnv) {
	case "":
		return
	case "hold":
		// The console's other client, as the shell that started dcctl is: a Ctrl+C typed
		// at the console reaches every process on it, and this one stays.
		signal.Ignore(os.Interrupt)
		time.Sleep(10 * time.Minute)
		os.Exit(0)
	case "probe":
		os.Exit(probeConsoleModes(os.Getenv(conptyOutEnv)))
	case "dcctl":
		var args []string
		for i := 0; ; i++ {
			a, ok := os.LookupEnv(fmt.Sprintf("%s_%d", conptyChildEnv, i))
			if !ok {
				break
			}
			args = append(args, a)
		}
		rootCmd.SetArgs(args)
		Execute()
		os.Exit(0)
	}
	os.Exit(3)
}

// probeConsoleModes writes the modes of the console's input buffer and active screen
// buffer to path. It opens CONIN$ and CONOUT$ rather than using its standard handles,
// so what it reads is the console's state whatever handles it was given.
func probeConsoleModes(path string) int {
	mode := func(name string) (uint32, error) {
		h, err := windows.CreateFile(windows.StringToUTF16Ptr(name), windows.GENERIC_READ|windows.GENERIC_WRITE,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
		if err != nil {
			return 0, err
		}
		defer windows.CloseHandle(h)
		var m uint32
		return m, windows.GetConsoleMode(h, &m)
	}
	in, err := mode("CONIN$")
	if err != nil {
		_ = os.WriteFile(path, []byte("error CONIN$: "+err.Error()), 0o644)
		return 2
	}
	out, err := mode("CONOUT$")
	if err != nil {
		_ = os.WriteFile(path, []byte("error CONOUT$: "+err.Error()), 0o644)
		return 2
	}
	if err := os.WriteFile(path, []byte(fmt.Sprintf("%d %d", in, out)), 0o644); err != nil {
		return 2
	}
	return 0
}

type consoleModes struct{ in, out uint32 }

// conPTY is one pseudo console, with the parent's ends of its two pipes.
type conPTY struct {
	hpc    windows.Handle
	input  *os.File // what is written here is typed at the console
	output *os.File // what the console draws, as virtual-terminal sequences

	mu     sync.Mutex
	screen bytes.Buffer
	done   chan struct{}
}

func newConPTY(t *testing.T, cols, rows int16) *conPTY {
	t.Helper()
	var inR, inW, outR, outW windows.Handle
	if err := windows.CreatePipe(&inR, &inW, nil, 0); err != nil {
		t.Fatal(err)
	}
	if err := windows.CreatePipe(&outR, &outW, nil, 0); err != nil {
		t.Fatal(err)
	}
	c := &conPTY{done: make(chan struct{})}
	if err := windows.CreatePseudoConsole(windows.Coord{X: cols, Y: rows}, inR, outW, 0, &c.hpc); err != nil {
		t.Skipf("no pseudo console on this Windows: %v", err)
	}
	// The pseudo console holds its own duplicates of these.
	_ = windows.CloseHandle(inR)
	_ = windows.CloseHandle(outW)
	c.input = os.NewFile(uintptr(inW), "conpty-input")
	c.output = os.NewFile(uintptr(outR), "conpty-output")
	// The console blocks when nobody reads what it draws, so it is read for its whole life.
	go func() {
		defer close(c.done)
		buf := make([]byte, 8192)
		for {
			n, err := c.output.Read(buf)
			c.mu.Lock()
			c.screen.Write(buf[:n])
			c.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		// Closing the pseudo console flushes what it still has to draw, so the reader
		// keeps reading until it ends.
		windows.ClosePseudoConsole(c.hpc)
		_ = c.input.Close()
		select {
		case <-c.done:
		case <-time.After(10 * time.Second):
			t.Error("the pseudo console's output never ended")
		}
		_ = c.output.Close()
	})
	return c
}

// conptyEnv is the whole environment of every child: no CI marker (a CI runner sets
// one, and with it the terminal UI is never chosen), no tools on PATH, and a home,
// configuration directory and kubeconfig of the test's own.
func conptyEnv(t *testing.T) []string {
	t.Helper()
	home := t.TempDir()
	sys := os.Getenv("SystemRoot")
	return []string{
		"SystemRoot=" + sys,
		"PATH=" + filepath.Join(sys, "System32"),
		"TEMP=" + os.TempDir(), "TMP=" + os.TempDir(),
		"USERPROFILE=" + home, "HOME=" + home,
		"APPDATA=" + filepath.Join(home, "AppData", "Roaming"),
		"LOCALAPPDATA=" + filepath.Join(home, "AppData", "Local"),
		"KUBECONFIG=" + filepath.Join(home, "no-kubeconfig"),
	}
}

// start runs this test binary as a child of the pseudo console, in the given role.
func (c *conPTY) start(t *testing.T, env []string, role string, args ...string) windows.Handle {
	t.Helper()
	env = append(append([]string(nil), env...), conptyChildEnv+"="+role)
	for i, a := range args {
		env = append(env, fmt.Sprintf("%s_%d=%s", conptyChildEnv, i, a))
	}
	return c.spawn(t, env, os.Args[0], "-test.run=^TestConPTYChild$", "-test.count=1")
}

func (c *conPTY) spawn(t *testing.T, env []string, argv ...string) windows.Handle {
	t.Helper()
	attrs, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		t.Fatal(err)
	}
	defer attrs.Delete()
	// The attribute's value is the pseudo console handle itself, not a pointer to it: its
	// bits are passed where a pointer goes (read through memory, so that go vet does not
	// take an integer turned into a pointer for a mistake).
	value := *(*unsafe.Pointer)(unsafe.Pointer(&c.hpc))
	if err := attrs.Update(windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE, value, unsafe.Sizeof(c.hpc)); err != nil {
		t.Fatal(err)
	}
	si := windows.StartupInfoEx{ProcThreadAttributeList: attrs.List()}
	si.Cb = uint32(unsafe.Sizeof(si))
	// No standard handles are given: the child's are the pseudo console's own, even when
	// this process's are pipes, as they are under go test.
	si.Flags = windows.STARTF_USESTDHANDLES
	cmdline, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(argv))
	if err != nil {
		t.Fatal(err)
	}
	var block []uint16
	for _, kv := range env {
		u, err := windows.UTF16FromString(kv) // NUL-terminated
		if err != nil {
			t.Fatal(err)
		}
		block = append(block, u...)
	}
	block = append(block, 0)
	var pi windows.ProcessInformation
	if err := windows.CreateProcess(nil, cmdline, nil, nil, false,
		windows.EXTENDED_STARTUPINFO_PRESENT|windows.CREATE_UNICODE_ENVIRONMENT, &block[0], nil, &si.StartupInfo, &pi); err != nil {
		t.Fatal(err)
	}
	_ = windows.CloseHandle(pi.Thread)
	t.Cleanup(func() {
		_ = windows.TerminateProcess(pi.Process, 9)
		_ = windows.CloseHandle(pi.Process)
	})
	return pi.Process
}

// wait returns the process's exit status, failing the test if it has not exited in time.
func (c *conPTY) wait(t *testing.T, proc windows.Handle, limit time.Duration) uint32 {
	t.Helper()
	ev, err := windows.WaitForSingleObject(proc, uint32(limit/time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if ev != windows.WAIT_OBJECT_0 {
		t.Fatalf("the process did not exit within %v; screen:\n%s", limit, c.text())
	}
	var code uint32
	if err := windows.GetExitCodeProcess(proc, &code); err != nil {
		t.Fatal(err)
	}
	return code
}

// probe reads the console's modes through a probe process on it.
func (c *conPTY) probe(t *testing.T, env []string) consoleModes {
	t.Helper()
	out := filepath.Join(t.TempDir(), "modes")
	proc := c.start(t, append(append([]string(nil), env...), conptyOutEnv+"="+out), "probe")
	if code := c.wait(t, proc, 30*time.Second); code != 0 {
		b, _ := os.ReadFile(out)
		t.Fatalf("the console probe exited %d: %s", code, b)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var m consoleModes
	if _, err := fmt.Sscanf(string(b), "%d %d", &m.in, &m.out); err != nil {
		t.Fatalf("the console probe wrote %q: %v", b, err)
	}
	return m
}

// awaitRawInput probes the console until the terminal UI has turned line input off, and
// returns the modes it saw then. It fails if dcctl exits first.
func (c *conPTY) awaitRawInput(t *testing.T, env []string, before consoleModes, proc windows.Handle) consoleModes {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	for {
		m := c.probe(t, env)
		if m.in&windows.ENABLE_LINE_INPUT == 0 && before.in&windows.ENABLE_LINE_INPUT != 0 {
			return m
		}
		if ev, _ := windows.WaitForSingleObject(proc, 0); ev == windows.WAIT_OBJECT_0 {
			t.Fatalf("dcctl exited before the terminal UI took the console; screen:\n%s", c.text())
		}
		if time.Now().After(deadline) {
			t.Fatalf("line input still on (mode %#x) %v after dcctl started; screen:\n%s", m.in, 45*time.Second, c.text())
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// awaitText waits until the console has drawn want. It fails if dcctl exits first.
func (c *conPTY) awaitText(t *testing.T, want string, proc windows.Handle) {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	for !strings.Contains(c.text(), want) {
		if ev, _ := windows.WaitForSingleObject(proc, 0); ev == windows.WAIT_OBJECT_0 {
			t.Fatalf("dcctl exited before it drew %q; screen:\n%s", want, c.text())
		}
		if time.Now().After(deadline) {
			t.Fatalf("dcctl never drew %q in %v; screen:\n%s", want, 45*time.Second, c.text())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// type_ types keys at the console.
func (c *conPTY) type_(t *testing.T, keys string) {
	t.Helper()
	if _, err := c.input.Write([]byte(keys)); err != nil {
		t.Fatal(err)
	}
}

var vtSequence = regexp.MustCompile("\x1b(\\[[0-9;?<=>]*[ -/]*[@-~]|\\][^\x07\x1b]*(\x07|\x1b\\\\)|[@-Z\\\\-_])")

// text is what the console drew, with its control sequences removed.
func (c *conPTY) text() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return vtSequence.ReplaceAllString(c.screen.String(), "")
}
