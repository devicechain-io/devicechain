// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/devicechain-io/dcctl/internal/installflow"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	clientauthv1 "k8s.io/client-go/pkg/apis/clientauthentication/v1"
	"k8s.io/client-go/rest"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// The advisory checks run a context's exec credential plugin THEMSELVES, never through
// client-go, which runs one with no way to stop it. Most managed clusters (GKE, EKS,
// AKS) authenticate through one, so without it the authentication and permission rows
// could only read NOT CHECKED. What makes running it safe:
//
//   - 🔴 IT IS BOUNDED AND IT DIES WITH EVERYTHING IT STARTED. It runs under the probe's
//     context, inside execPluginTimeout, in its own process tree (a session and process
//     group on Unix, a Job Object on Windows, procTree): an Exit or the deadline kills the
//     whole tree at once, and so does the plugin's own exit, so a child it leaves behind
//     (holding its output open, say) neither holds the run nor outlives it. The run is
//     joined boundedly (within), so nothing it does holds an Exit past commandWaitDelay.
//   - 🔴 IT IS NEVER INTERACTIVE. stdin is the null device, the plugin has no controlling
//     terminal (Unix: a new session; Windows: no console window), KUBERNETES_EXEC_INFO
//     tells it it is not interactive, and a plugin whose entry says interactiveMode
//     Always is not run at all: NOT CHECKED.
//   - 🔴 ITS OUTPUT IS NEVER SHOWN OR TRUSTED. stdout is read up to execStdoutCap and only
//     ever parsed as an ExecCredential of the entry's API version; a credential already
//     expired is not used, and one that expires during the run is refused by kubeGuard.
//     Only stderr is ever shown, on a failure, capped and with every URL's credentials
//     removed (installflow.RedactURLsInText). The credential reaches the API server
//     through the SAME guarded client as static credentials (cloudKubeClientUntil): no
//     redirect, no proxy, the exact request allowlist, the context's server only.
//   - 🔴 IT NEVER PASSES ON ITS OWN. A plugin that is refused, fails, hangs or answers
//     nonsense reads NOT CHECKED or UNKNOWN (WARN when it is not installed at all), and
//     authentication passes only on the API server's word, as for any credentials.
//
// auth-provider entries (the legacy gcp and azure plugins) are never run: credPlugin.

// execPluginTimeout is how long a credential plugin may take, inside the probes' budget.
// A variable only so a test can make a hung plugin give up quickly.
var execPluginTimeout = 10 * time.Second

const (
	// execStdoutCap is the most of a plugin's stdout that is read: an ExecCredential with
	// a certificate chain is a few kilobytes. More is refused, never parsed.
	execStdoutCap = 1 << 20
	// execStderrCap is the most of its stderr that is kept, and execSaidCap the most of
	// that a row shows.
	execStderrCap = 4 << 10
	execSaidCap   = 240
)

// The ExecCredential API versions client-go still speaks.
const (
	execAPIv1      = "client.authentication.k8s.io/v1"
	execAPIv1beta1 = "client.authentication.k8s.io/v1beta1"
)

// execOutcome is what running a plugin came to.
type execOutcome uint8

const (
	execOK      execOutcome = iota // a usable credential
	execNotRun                     // not run, by design: NOT CHECKED
	execMissing                    // not installed on this machine: WARN
	execFailed                     // run, and gave nothing usable: UNKNOWN
)

// execResult is a plugin run: the credential when there is one, or dcctl's sentence for
// why there is none (why) and the plugin's own words (said, redacted).
type execResult struct {
	outcome execOutcome
	why     string
	said    string

	token     string
	cert, key []byte
	expires   time.Time
}

// execPlugin is a context's exec credential plugin as the checks run it, read entirely
// when the context is loaded, so running it reads no file.
type execPlugin struct {
	name       string // the command's base name, for rows
	command    string
	args       []string
	env        []string
	apiVersion string
	// refuse, when set, is why the plugin is not run (NOT CHECKED).
	refuse string
}

// execPluginFor is cfg's exec plugin. cfg's TLS files must already be loaded into it
// (rest.LoadTLSFiles), since the cluster information a plugin may ask for carries the
// certificate authority.
func execPluginFor(cfg *rest.Config) execPlugin {
	e := cfg.ExecProvider
	x := execPlugin{name: filepath.Base(e.Command), command: e.Command, args: e.Args, apiVersion: e.APIVersion}
	plugin := "the credential plugin " + x.name
	switch e.APIVersion {
	case execAPIv1, execAPIv1beta1:
	default:
		x.refuse = plugin + " speaks an API version the advisory checks do not"
		return x
	}
	mode := e.InteractiveMode
	if mode == "" && e.APIVersion == execAPIv1beta1 {
		mode = clientcmdapi.IfAvailableExecInteractiveMode // as client-go defaults it
	}
	switch mode {
	case clientcmdapi.NeverExecInteractiveMode, clientcmdapi.IfAvailableExecInteractiveMode:
		// Run, never interactively: as client-go runs one when no terminal is attached.
	case clientcmdapi.AlwaysExecInteractiveMode:
		x.refuse = plugin + " must be run interactively, and the advisory checks never run a plugin at a terminal"
		return x
	default:
		x.refuse = plugin + "'s entry does not say whether it may be interactive (interactiveMode), so it is not run"
		return x
	}
	info := clientauthv1.ExecCredential{
		TypeMeta: metav1.TypeMeta{APIVersion: e.APIVersion, Kind: "ExecCredential"},
		Spec:     clientauthv1.ExecCredentialSpec{Interactive: false},
	}
	if e.ProvideClusterInfo {
		c, err := execClusterInfo(cfg)
		if err != nil {
			x.refuse = plugin + " asks for the cluster's details, and they could not be read"
			return x
		}
		info.Spec.Cluster = c
	}
	b, err := json.Marshal(info)
	if err != nil {
		x.refuse = plugin + " could not be given its exec information"
		return x
	}
	// The environment exactly as client-go builds it: this process's own, then the
	// entry's, then the exec information. (A later entry wins.)
	x.env = os.Environ()
	for _, v := range e.Env {
		x.env = append(x.env, v.Name+"="+v.Value)
	}
	x.env = append(x.env, "KUBERNETES_EXEC_INFO="+string(b))
	return x
}

// execClusterInfo is the cluster information client-go gives a plugin that asks for it
// (rest.ConfigToExecCluster), in the versioned form.
func execClusterInfo(cfg *rest.Config) (*clientauthv1.Cluster, error) {
	c := &clientauthv1.Cluster{Server: cfg.Host, TLSServerName: cfg.ServerName, InsecureSkipTLSVerify: cfg.Insecure,
		CertificateAuthorityData: cfg.CAData, DisableCompression: cfg.DisableCompression}
	if cfg.Proxy != nil {
		req, err := http.NewRequest("", cfg.Host, nil)
		if err != nil {
			return nil, err
		}
		u, err := cfg.Proxy(req)
		if err != nil {
			return nil, err
		}
		if u != nil {
			c.ProxyURL = u.String()
		}
	}
	if obj := cfg.ExecProvider.Config; obj != nil {
		if u, ok := obj.(*runtime.Unknown); ok {
			c.Config.Raw = u.Raw
		} else {
			raw, err := json.Marshal(obj)
			if err != nil {
				return nil, err
			}
			c.Config.Raw = raw
		}
	}
	return c, nil
}

// run runs the plugin, bounded by ctx and execPluginTimeout, and returns what it gave.
func (x execPlugin) run(ctx context.Context) execResult {
	if x.refuse != "" {
		return execResult{outcome: execNotRun, why: x.refuse}
	}
	res, err := within(ctx, execPluginTimeout, x.runBounded)
	if err != nil {
		return execResult{outcome: execFailed, why: "the credential plugin " + x.name + " did not finish within " + execPluginTimeout.String()}
	}
	return res
}

// runBounded is one run of the plugin, killed with everything it started when ctx ends
// or when it exits, whichever is first.
func (x execPlugin) runBounded(ctx context.Context) (execResult, error) {
	plugin := "the credential plugin " + x.name
	failed := func(why string) execResult { return execResult{outcome: execFailed, why: plugin + " " + why} }
	// 🔴 The plugin writes to pipes dcctl made, not to ones os/exec makes and drains: os/exec
	// waits for its pipes to close, which a child holding one open delays until WaitDelay.
	// With these, Wait returns when the plugin exits, the tree is killed at once, and the
	// pipes close with it.
	outR, outW, err := os.Pipe()
	if err != nil {
		return failed("could not be started"), nil
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		_, _ = outR.Close(), outW.Close()
		return failed("could not be started"), nil
	}
	closeAll := func() { _, _, _, _ = outR.Close(), outW.Close(), errR.Close(), errW.Close() }
	cmd := exec.CommandContext(ctx, x.command, x.args...)
	cmd.Env = x.env
	cmd.Stdin = nil // 🔴 the null device: nothing to prompt on
	cmd.Stdout, cmd.Stderr = outW, errW
	cmd.WaitDelay = commandWaitDelay
	tree := newProcTree(cmd)
	if err := tree.start(); err != nil {
		closeAll()
		switch {
		case errors.Is(err, exec.ErrDot):
			return failed("was found only in the current directory, and is not run from there"), nil
		case errors.Is(err, errNoProcessTree):
			return execResult{outcome: execNotRun, why: plugin + " is not run: this system gives the advisory checks no way to stop it"}, nil
		case errors.Is(err, exec.ErrNotFound), errors.Is(err, fs.ErrNotExist):
			return execResult{outcome: execMissing, why: plugin + " that this context runs is not installed on this machine"}, nil
		}
		return failed("could not be started"), nil
	}
	_, _ = outW.Close(), errW.Close() // the plugin's copies are its own now
	out, said := &cappedBuffer{max: execStdoutCap}, &cappedBuffer{max: execStderrCap}
	var readers sync.WaitGroup
	for _, c := range []struct {
		dst *cappedBuffer
		src *os.File
	}{{out, outR}, {said, errR}} {
		readers.Add(1)
		go func() {
			defer readers.Done()
			_, _ = io.Copy(c.dst, c.src)
		}()
	}
	waitErr := cmd.Wait()
	tree.kill() // 🔴 whatever it started dies with it
	// The tree is dead, so the pipes close; a descendant that escaped the tree could still
	// hold one, and is given up on after commandWaitDelay.
	readersDone := make(chan struct{})
	go func() { readers.Wait(); close(readersDone) }()
	select {
	case <-readersDone:
	case <-time.After(commandWaitDelay):
		_, _ = outR.Close(), errR.Close()
		<-readersDone
	}
	_, _ = outR.Close(), errR.Close()
	tree.close()
	if err := ctx.Err(); err != nil {
		return execResult{}, err
	}
	var exitErr *exec.ExitError
	switch {
	case errors.As(waitErr, &exitErr):
		r := failed(fmt.Sprintf("exited with status %d", exitErr.ExitCode()))
		r.said = pluginSaid(said.String())
		return r, nil
	case waitErr != nil:
		return failed("did not finish cleanly"), nil
	case out.overflow:
		return failed("printed more than an ExecCredential, which is not read"), nil
	}
	return parseExecCredential(x, out.Bytes(), time.Now()), nil
}

// parseExecCredential is the credential a plugin printed, checked as client-go checks it,
// and as of now: an expired one is not used.
func parseExecCredential(x execPlugin, out []byte, now time.Time) execResult {
	plugin := "the credential plugin " + x.name
	unusable := execResult{outcome: execFailed, why: plugin + " gave no usable credential"}
	var c clientauthv1.ExecCredential // v1beta1's has the same shape
	if err := json.Unmarshal(out, &c); err != nil {
		return unusable
	}
	if c.APIVersion != x.apiVersion || c.Kind != "ExecCredential" || c.Status == nil {
		return unusable
	}
	s := c.Status
	hasCert, hasKey := s.ClientCertificateData != "", s.ClientKeyData != ""
	if hasCert != hasKey || (s.Token == "" && !hasCert) {
		return unusable
	}
	r := execResult{outcome: execOK, token: s.Token}
	if hasCert {
		r.cert, r.key = []byte(s.ClientCertificateData), []byte(s.ClientKeyData)
	}
	if s.ExpirationTimestamp != nil {
		r.expires = s.ExpirationTimestamp.Time
		if !now.Before(r.expires) {
			return execResult{outcome: execFailed, why: plugin + " returned a credential that has already expired"}
		}
	}
	return r
}

// pluginSaid is a plugin's stderr as a row may show it: the credential parts of every
// URL removed, on one line, capped.
func pluginSaid(stderr string) string {
	s := strings.Join(strings.Fields(installflow.RedactURLsInText(stderr)), " ")
	if len(s) > execSaidCap {
		cut := execSaidCap
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut] + "…"
	}
	return s
}

// cappedBuffer keeps the first max bytes written to it and notes whether there were
// more. It always accepts a write, so a plugin that prints too much is drained to its
// end rather than left blocked on a full pipe.
type cappedBuffer struct {
	mu       sync.Mutex
	buf      []byte
	max      int
	overflow bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	room := b.max - len(b.buf)
	if room < len(p) {
		b.overflow = true
		if room > 0 {
			b.buf = append(b.buf, p[:room]...)
		}
		return len(p), nil
	}
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *cappedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf...)
}

func (b *cappedBuffer) String() string { return string(b.Bytes()) }

// errNoProcessTree is a system on which a plugin cannot be run as a tree dcctl can kill.
var errNoProcessTree = errors.New("no process tree on this system")

// execAuthenticator is how cloudProbesFrom authenticates a context whose credentials come
// from an exec plugin: it runs the plugin and, on a usable credential, returns a cluster
// client carrying it, built exactly as one for static credentials (cloudKubeClientUntil).
// base must have its TLS files loaded already, so nothing here reads a file.
func execAuthenticator(life context.Context, base *rest.Config) func(context.Context) (kubernetes.Interface, execResult) {
	plugin := execPluginFor(base)
	return func(ctx context.Context) (kubernetes.Interface, execResult) {
		res := plugin.run(ctx)
		if res.outcome != execOK {
			return nil, res
		}
		c := rest.CopyConfig(base)
		c.ExecProvider, c.AuthProvider = nil, nil
		c.BearerToken, c.BearerTokenFile = res.token, ""
		if len(res.cert) > 0 {
			c.CertData, c.KeyData, c.CertFile, c.KeyFile = res.cert, res.key, "", ""
		}
		kube, err := cloudKubeClientUntil(life, c, res.expires)
		if err != nil {
			return nil, execResult{outcome: execFailed, why: "the credential plugin " + plugin.name + " gave no usable credential"}
		}
		return kube, res
	}
}
