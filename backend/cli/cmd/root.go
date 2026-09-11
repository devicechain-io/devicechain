// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

// Build stamps, injected via ldflags by the two real build paths: the makefile
// (developer builds) and goreleaser (released binaries) — keep the two in sync.
// Every one of these is best-effort: a `go build ./...` or `go run .` that
// bypasses both leaves them empty, so `dcctl version` must render without them
// rather than print a confidently wrong answer.
var (
	// gitCommit is the full hash of HEAD at build time.
	gitCommit string

	// gitTreeState is "clean" or "dirty" — whether the working tree carried
	// uncommitted changes at build time. A dirty build's commit hash does not
	// identify its source, which is precisely when you most need to be told.
	gitTreeState string

	// buildDate is an RFC 3339 UTC timestamp of the build. `version` renders a
	// relative age from it: the stale-binary failure mode is invisible to a
	// version string alone, because a version can repeat across builds while an
	// age cannot.
	buildDate string
)

// Version is the build version, injected via ldflags at build time. A release
// build gets the git tag from goreleaser; every makefile build gets the
// repo-root VERSION file with a "-dev.<utc-timestamp>" suffix, because that file
// is a slow-moving constant — pre-GA it has read 0.0.1 for the project's entire
// history, so without the suffix every local build ever made reports an
// identical version and no two can be told apart.
//
// For local builds this deliberately does NOT match bootstrap.DefaultImageVersion,
// which must stay a tag that resolves in a registry and is therefore left at its
// "dev" fallback rather than stamped from a file; see that value's docs.
// `version` prints both.
var Version = "dev"

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "dcctl",
	Short: "DeviceChain CLI",
	Long: color.HiGreenString(`
    ____            _           ________          _     
   / __ \___ _   __(_)_______  / ____/ /_  ____ _(_)___ 
  / / / / _ \ | / / / ___/ _ \/ /   / __ \/ __  / / __ \
 / /_/ /  __/ |/ / / /__/  __/ /___/ / / / /_/ / / / / /
/_____/\___/|___/_/\___/\___/\____/_/ /_/\__,_/_/_/ /_/ 
                                                        
Command line interface for interacting with DeviceChain components`),
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
//
// 🔴 THE SIGNAL HANDLING IS NOT HOUSEKEEPING — WITHOUT IT, CTRL+C CORRUPTS AN
// APPLY ON LINUX. terraform-exec starts the child in its own process group with
// Pdeathsig set to SIGKILL, and it does so ONLY on Linux (cmd_linux.go; the
// cmd_default.go build for every other platform sets neither). That combination
// means the terminal's SIGINT never reaches tofu: it goes to dcctl, dcctl has no
// handler and dies, and the kernel then SIGKILLs tofu — the one way to stop an
// apply that guarantees it cannot write its state file, leaving every resource
// created since the last write untracked.
//
// The platform restriction is worth stating rather than glossing, because dcctl
// ships darwin builds and the reasoning does not carry over: there, tofu shares
// dcctl's process group and already receives the terminal's interrupt directly.
// The handler below is still correct on that platform — it is what returns the
// cluster lock — but the corruption it prevents is a Linux one.
//
// Cancelling the context instead produces the opposite outcome. terraform-exec
// sets cmd.Cancel to send os.Interrupt, so tofu gets its graceful-stop signal,
// finishes the operation in flight and writes state before exiting.
//
// It is also what lets a run give up the cluster lock it holds. Without a
// cancellable context the deferred release never runs, and every interrupted
// bootstrap would strand the lock until another operator waited out a full lease
// duration and reclaimed it by hand.
//
// 🔴 THE SECOND SIGNAL NEEDS THE GOROUTINE BELOW, AND ASSUMING OTHERWISE WAS A
// REAL TRAP RATHER THAN A DOCUMENTATION SLIP. signal.NotifyContext does NOT
// restore the default disposition when the context is cancelled — it keeps the
// handler registered until stop() is called, and stop() here is deferred until
// the command returns. So every later Ctrl+C is swallowed, silently. That was
// measured, not assumed: a program with this exact shape survived three SIGINTs.
//
// It matters because the graceful stop is allowed to take a long time on purpose
// (see tofuGracefulStopBudget), and that budget was justified BY the escape
// hatch. Without one, an operator interrupting a stuck apply is pinned for twenty
// minutes with nothing to do but `kill -9` from another terminal — which trips
// Pdeathsig and SIGKILLs tofu, losing exactly the state file this whole
// arrangement exists to protect. The false claim did not merely describe the
// wrong behaviour; it was the argument for the number that made it harmful.
//
// Calling stop() once the context is cancelled restores the default disposition,
// so the second signal terminates the process the way an impatient operator
// expects. Someone who has decided they want out now must not be made to argue
// with a cleanup path.
func Execute() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		stop()
	}()

	if err := rootCmd.ExecuteContext(ctx); err != nil {
		os.Exit(1)
	}
}

func init() {
	// Here you will define your flags and configuration settings.
	// Cobra supports persistent flags, which, if defined here,
	// will be global for your application.

	// rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is $HOME/.dcctl.yaml)")

	// Cobra also supports local flags, which will only run
	// when this action is called directly.
	// rootCmd.Flags().BoolP("toggle", "t", false, "Help message for toggle")
}
