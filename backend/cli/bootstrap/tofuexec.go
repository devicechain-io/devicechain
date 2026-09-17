// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"io"
	"os"

	"github.com/hashicorp/terraform-exec/tfexec"
)

// tofuExec is the one way dcctl builds a tofu runner. It embeds terraform-exec's
// Terraform and overrides only the commands whose stdout an operator wants to watch:
// Init, Apply, Destroy and StateRm stream it for their own duration; everything else
// — Show, Output, Import, Version — is promoted unchanged and prints nothing.
//
// 🔴 A READ MUST NEVER STREAM STDOUT. terraform-exec copies the stdout of EVERY
// command to the writer SetStdout names, and a read's stdout is its result: `show
// -json` is the whole state and `output -json` every output, verbatim — sensitive
// attributes included, on the operator's terminal and in CI logs. So stdout is
// discarded by default and switched on per progress command, never set once on the
// runner. Stderr stays streamed for everything; it carries tofu's diagnostics.
//
// Not safe for concurrent use: the stream is a field on the shared runner. Every
// root runs its commands one after another.
type tofuExec struct {
	*tfexec.Terraform
}

// newTofuExec builds the runner for one root with stdout quiet and stderr streamed.
func newTofuExec(rootdir, tofuBin string) (*tofuExec, error) {
	tf, err := tfexec.NewTerraform(rootdir, tofuBin)
	if err != nil {
		return nil, err
	}
	tf.SetStdout(io.Discard)
	tf.SetStderr(os.Stderr)
	return &tofuExec{Terraform: tf}, nil
}

// streaming runs a progress command with stdout on the terminal, so a long apply is
// not a silent wait.
//
// 🔴 THE VERSION IS READ FIRST, WHILE STDOUT IS STILL QUIET. terraform-exec runs
// `version -json` inside Init (and inside other commands for some options) the first
// time it needs the version, and that command's stdout is merged into the same
// writer — so without this the first Init prints a JSON document. The result is
// cached on the runner, so this costs one call per root, not one per command.
func (t *tofuExec) streaming(ctx context.Context, run func() error) error {
	if _, _, err := t.Version(ctx, false); err != nil {
		return err
	}
	t.SetStdout(os.Stdout)
	defer t.SetStdout(io.Discard)
	return run()
}

func (t *tofuExec) Init(ctx context.Context, opts ...tfexec.InitOption) error {
	return t.streaming(ctx, func() error { return t.Terraform.Init(ctx, opts...) })
}

func (t *tofuExec) Apply(ctx context.Context, opts ...tfexec.ApplyOption) error {
	return t.streaming(ctx, func() error { return t.Terraform.Apply(ctx, opts...) })
}

func (t *tofuExec) Destroy(ctx context.Context, opts ...tfexec.DestroyOption) error {
	return t.streaming(ctx, func() error { return t.Terraform.Destroy(ctx, opts...) })
}

func (t *tofuExec) StateRm(ctx context.Context, address string, opts ...tfexec.StateRmCmdOption) error {
	return t.streaming(ctx, func() error { return t.Terraform.StateRm(ctx, address, opts...) })
}
