// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package tofurun

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestMain lets the test binary stand in for a `tofu` binary: copied under that name beside a
// version.txt, it prints the file for `version` and exits. That is how the binary-identity rules
// are tested without a real Terraform or a different OpenTofu.
func TestMain(m *testing.M) {
	base := strings.TrimSuffix(strings.ToLower(filepath.Base(os.Args[0])), ".exe")
	if base == "tofu" && len(os.Args) > 1 && os.Args[1] == "version" {
		b, err := os.ReadFile(filepath.Join(filepath.Dir(os.Args[0]), "version.txt"))
		if err != nil {
			os.Exit(3)
		}
		_, _ = os.Stdout.Write(b)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fakeBinary(t *testing.T, name, versionOutput string) string {
	t.Helper()
	dir := t.TempDir()
	self, err := os.Executable()
	require.NoError(t, err)
	dst := filepath.Join(dir, name)
	src, err := os.Open(self)
	require.NoError(t, err)
	defer src.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY, 0o755)
	require.NoError(t, err)
	_, err = io.Copy(out, src)
	require.NoError(t, err)
	require.NoError(t, out.Close())
	require.NoError(t, os.WriteFile(filepath.Join(dir, "version.txt"), []byte(versionOutput), 0o600))
	return dst
}

func ext() string {
	if os.PathSeparator == '\\' {
		return ".exe"
	}
	return ""
}

func TestBinaryIdentity(t *testing.T) {
	ctx := context.Background()

	t.Run("OpenTofu at the expected version is accepted", func(t *testing.T) {
		bin := fakeBinary(t, "tofu"+ext(), "OpenTofu v1.10.10\non linux_amd64\n")
		got, err := ResolveTofu(bin)
		require.NoError(t, err)
		require.NoError(t, CheckVersion(ctx, got, "1.10.10"))
	})
	t.Run("another OpenTofu version is refused", func(t *testing.T) {
		bin := fakeBinary(t, "tofu"+ext(), "OpenTofu v1.10.6\n")
		require.ErrorIs(t, CheckVersion(ctx, bin, "1.10.10"), ErrVersionMismatch)
	})
	t.Run("Terraform under the name tofu is refused", func(t *testing.T) {
		bin := fakeBinary(t, "tofu"+ext(), "Terraform v1.5.7\non linux_amd64\n")
		require.ErrorIs(t, CheckVersion(ctx, bin, "1.5.7"), ErrNotTofu)
	})
	t.Run("a terraform-named binary is refused before it is run", func(t *testing.T) {
		bin := fakeBinary(t, "terraform"+ext(), "OpenTofu v1.10.10\n")
		_, err := ResolveTofu(bin)
		require.ErrorIs(t, err, ErrNotTofu)
	})
	t.Run("a relative path is refused", func(t *testing.T) {
		_, err := ResolveTofu("tofu")
		require.ErrorIs(t, err, ErrNotTofu)
	})
	t.Run("a host with only terraform on PATH finds no tofu", func(t *testing.T) {
		dir := filepath.Dir(fakeBinary(t, "terraform"+ext(), "Terraform v1.5.7\n"))
		t.Setenv("PATH", dir)
		_, err := ResolveTofu("")
		require.ErrorIs(t, err, ErrNotTofu)
	})
	t.Run("an empty expected version is refused", func(t *testing.T) {
		bin := fakeBinary(t, "tofu"+ext(), "OpenTofu v1.10.10\n")
		require.ErrorIs(t, CheckVersion(ctx, bin, ""), ErrVersionMismatch)
	})
}

func TestNewRequiresTheFenceAndCredentials(t *testing.T) {
	_, err := New(context.Background(), Config{WorkRoot: t.TempDir(), Credentials: noCreds{}})
	require.ErrorIs(t, err, ErrNoFence)
	_, err = New(context.Background(), Config{WorkRoot: t.TempDir(), Fence: nopFence{}})
	require.Error(t, err)
}

type noCreds struct{}

func (noCreds) For(IdentityClass) (Credential, error) { return Credential{}, fmt.Errorf("none") }

type nopFence struct{}

func (nopFence) Acquire(context.Context, RunInfo) error           { return nil }
func (nopFence) Started(context.Context, RunInfo) error           { return nil }
func (nopFence) Complete(context.Context, RunInfo, Outcome) error { return nil }

func TestClassFor(t *testing.T) {
	for k, want := range map[Kind]IdentityClass{
		KindCreate: ClassApply, KindChange: ClassApply, KindUpgrade: ClassApply, KindDriftCheck: ClassApply,
		KindRecover: ClassApply, KindDestroy: ClassDestroy,
	} {
		got, err := ClassFor(k)
		require.NoError(t, err)
		require.Equal(t, want, got, k)
	}
	_, err := ClassFor("DETACH")
	require.ErrorIs(t, err, ErrUnknownKind)
	_, err = ClassFor("")
	require.ErrorIs(t, err, ErrUnknownKind)
}

func TestCredentialNeverPrintsAValue(t *testing.T) {
	c, err := NewCredential(ClassApply, "fleet-apply@example", map[string]string{"GOOGLE_OAUTH_ACCESS_TOKEN": "s3cr3t-token-value"})
	require.NoError(t, err)
	for _, s := range []string{c.String(), c.GoString(), fmt.Sprintf("%v", c), fmt.Sprintf("%+v", c), fmt.Sprintf("%#v", c), fmt.Sprintf("%s", c)} {
		require.NotContains(t, s, "s3cr3t-token-value")
	}
	b, err := json.Marshal(c)
	require.NoError(t, err)
	require.NotContains(t, string(b), "s3cr3t-token-value")

	for _, bad := range []string{"PATH", "HOME", "PG_CONN_STR", "TF_DATA_DIR", "TF_VAR_x", "TF_ENCRYPTION", "lower", "A B", "", "FLEET_FAKE_CLOUD_TOKEN", "AWS_SECRET_ACCESS_KEY", "LD_PRELOAD", "GOOGLE_A B"} {
		_, err := NewCredential(ClassApply, "p", map[string]string{bad: "v"})
		require.Error(t, err, bad)
	}
	_, err = NewCredential("plan", "p", nil)
	require.ErrorIs(t, err, ErrIdentityClass)
	_, err = NewCredential(ClassApply, " ", nil)
	require.Error(t, err)
}

const clusterUID = "11111111-1111-1111-1111-111111111111"

func TestBackends(t *testing.T) {
	ctx := context.Background()
	run := RunInfo{ClusterUID: clusterUID}

	t.Run("pg: schema argument, secret connection string only in the environment, workspace per cluster", func(t *testing.T) {
		b := PGBackend{ConnStr: "postgres://u:pw-secret-123@h:5432/d", Schema: "tf_prov"}
		c, err := b.Config(ctx, run)
		require.NoError(t, err)
		require.Equal(t, []string{"-backend-config=schema_name=tf_prov"}, c.Args)
		require.Equal(t, "postgres://u:pw-secret-123@h:5432/d", c.Env["PG_CONN_STR"])
		require.Equal(t, clusterUID, c.Workspace)
		require.NotContains(t, b.String(), "pw-secret-123")
		require.NotContains(t, fmt.Sprintf("%#v %v %+v", b, b, b), "pw-secret-123")
	})
	t.Run("pg: refusals", func(t *testing.T) {
		for _, b := range []PGBackend{
			{ConnStr: "postgres://u:p@h/d", Schema: "Tf-Prov"},
			{ConnStr: "postgres://u:p@h/d", Schema: ""},
			{ConnStr: "mysql://u:p@h/d", Schema: "tf"},
			{ConnStr: "host=h user=u", Schema: "tf"},
		} {
			_, err := b.Config(ctx, run)
			require.Error(t, err)
		}
		_, err := PGBackend{ConnStr: "postgres://u:p@h/d", Schema: "tf"}.Config(ctx, RunInfo{ClusterUID: "x/../y"})
		require.Error(t, err)
	})
	t.Run("gcs: the prefix is derived from the cluster uid, never supplied", func(t *testing.T) {
		c, err := GCSBackend{Bucket: "provider-fleet-state"}.Config(ctx, RunInfo{ClusterUID: strings.ToUpper(clusterUID)})
		require.NoError(t, err)
		require.Equal(t, []string{"-backend-config=bucket=provider-fleet-state", "-backend-config=prefix=fleet/" + clusterUID}, c.Args)
		require.Empty(t, c.Workspace)
		require.Empty(t, c.Env)
	})
	t.Run("gcs: refusals", func(t *testing.T) {
		for _, bucket := range []string{"", "A", "Has Space", "../x", "gs://bucket"} {
			_, err := GCSBackend{Bucket: bucket}.Config(ctx, run)
			require.Error(t, err, bucket)
		}
	})
}

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	}
	return dir
}

func TestModuleInspection(t *testing.T) {
	good := map[string]string{"main.tf": "# root\n", ".terraform.lock.hcl": "# lock\n", "mod/x.tf": "# x\n"}

	t.Run("digest is stable, order-independent and sees every byte", func(t *testing.T) {
		a, la, err := inspectModule(writeTree(t, good))
		require.NoError(t, err)
		b, lb, err := inspectModule(writeTree(t, good))
		require.NoError(t, err)
		require.Equal(t, a, b)
		require.Equal(t, la, lb)
		require.NotEmpty(t, la)
		changed := map[string]string{"main.tf": "# root!\n", ".terraform.lock.hcl": "# lock\n", "mod/x.tf": "# x\n"}
		c, lc, err := inspectModule(writeTree(t, changed))
		require.NoError(t, err)
		require.NotEqual(t, a, c)
		require.Equal(t, la, lc, "the lock hash covers only the lock file")
		d, _, err := inspectModule(writeTree(t, map[string]string{"main.tf": "# root\n", ".terraform.lock.hcl": "# lock\n", "mod/y.tf": "# x\n"}))
		require.NoError(t, err)
		require.NotEqual(t, a, d, "the path is part of the digest")
	})
	t.Run("a root with no lock file has an empty lock hash", func(t *testing.T) {
		_, l, err := inspectModule(writeTree(t, map[string]string{"main.tf": "# r\n"}))
		require.NoError(t, err)
		require.Empty(t, l)
	})
	t.Run("state, plans, variable files, overrides, .terraform and CLI config are refused", func(t *testing.T) {
		for name := range map[string]string{
			"terraform.tfstate": "x", "terraform.tfstate.backup": "x", "x.tfplan": "x", "prod.tfvars": "x", "a.auto.tfvars": "x",
			"x.tfvars.json": "x", "override.tf": "x", "b_override.tf": "x", ".terraform/modules/m.json": "x", ".terraformrc": "x",
		} {
			_, _, err := inspectModule(writeTree(t, map[string]string{"main.tf": "#\n", name: "x"}))
			require.ErrorIs(t, err, ErrModuleRefused, name)
		}
	})
	t.Run("an empty tree, a missing directory and a file are refused", func(t *testing.T) {
		_, _, err := inspectModule(t.TempDir())
		require.ErrorIs(t, err, ErrModuleRefused)
		_, _, err = inspectModule(filepath.Join(t.TempDir(), "nope"))
		require.ErrorIs(t, err, ErrModuleRefused)
		_, _, err = inspectModule("")
		require.ErrorIs(t, err, ErrModuleRefused)
	})
	t.Run("a symbolic link is refused", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			return // creating a link needs a privilege a developer box may lack; the Linux legs run it
		}
		dir := writeTree(t, map[string]string{"main.tf": "#\n"})
		require.NoError(t, os.Symlink(filepath.Join(dir, "main.tf"), filepath.Join(dir, "link.tf")))
		_, _, err := inspectModule(dir)
		require.ErrorIs(t, err, ErrModuleRefused)
	})
	t.Run("the root path picks the lock file and may not leave the tree", func(t *testing.T) {
		tree := writeTree(t, map[string]string{"roots/a/main.tf": "#\n", "roots/a/.terraform.lock.hcl": "# a\n", "roots/b/main.tf": "#\n", "modules/m/x.tf": "#\n"})
		_, la, err := inspectTree(tree, "roots/a")
		require.NoError(t, err)
		require.NotEmpty(t, la)
		_, lb, err := inspectTree(tree, "roots/b")
		require.NoError(t, err)
		require.Empty(t, lb, "another root's lock file is not this root's")
		_, lt, err := inspectTree(tree, "")
		require.NoError(t, err)
		require.Empty(t, lt)
		for _, bad := range []string{"..", "../x", "/abs", "roots/../../x", `roots\a`, "roots/missing", "roots/a/main.tf"} {
			_, _, err := inspectTree(tree, bad)
			require.ErrorIs(t, err, ErrModuleRefused, bad)
		}
		require.NoError(t, CheckRoot(tree, "roots/a"))
		require.ErrorIs(t, CheckRoot(tree, "roots/b"), ErrModuleRefused, "a baked root without a lock file would resolve providers at run time")
	})
	t.Run("a copy must carry the digest the run was admitted with", func(t *testing.T) {
		src := writeTree(t, good)
		want, _, err := inspectModule(src)
		require.NoError(t, err)
		dst := t.TempDir()
		require.NoError(t, copyModule(src, dst, want))
		require.NoError(t, os.WriteFile(filepath.Join(src, "main.tf"), []byte("# changed after admission\n"), 0o644))
		require.ErrorIs(t, copyModule(src, t.TempDir(), want), ErrModuleRefused)
	})
}

func TestScrub(t *testing.T) {
	require.Equal(t, "dial *** failed: ***", scrub("dial postgres://u:pw@h failed: tok-123", []string{"postgres://u:pw@h", "tok-123"}))
	require.Equal(t, "short abc", scrub("short abc", []string{"abc"}), "a value under four bytes is not scrubbed (it would mangle every message)")
}
