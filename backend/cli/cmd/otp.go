// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/devicechain-io/dc-microservice/userclient"
)

// otpFlagHelp is the --otp usage text, shared so every command that takes a password says the same.
const otpFlagHelp = "one-time code (or a recovery code) for an account with two-factor authentication; " +
	"used for the first sign-in only. A terminal is prompted instead when none is given"

// promptOTP reads a code from the terminal without echoing it. It is a seam: `go test` hands the
// test binary the developer's own terminal, so a test that expects "no terminal" must be able to
// say so (see stdinIsTerminal).
var promptOTP = func() (string, error) {
	fmt.Fprint(os.Stderr, "One-time code: ")
	raw, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}

// otpFunc is how a command answers an account's second-factor challenge: the OTP function its
// sessions are given. It is ALWAYS supplied, even with no --otp, because the alternative (no
// function) fails with an error that does not name the flag.
//
// A code works once. The first sign-in uses --otp when given; a LATER one (the session's token
// expired and it has to sign in again) prompts when there is a terminal, and otherwise stops with
// an error that names --otp. It never offers the same code twice -- the server would refuse it as a
// replay, and a retry loop on top of that is how an account gets locked.
func otpFunc(flagValue string) userclient.OTPFunc {
	flagValue = strings.TrimSpace(flagValue)
	used := false
	return func(ctx context.Context, c *userclient.MfaRequiredError) (string, error) {
		if flagValue != "" && !used {
			used = true
			return flagValue, nil
		}
		if stdinIsTerminal() {
			code, err := promptOTP()
			if err != nil {
				return "", fmt.Errorf("reading the one-time code: %w", err)
			}
			if code == "" {
				return "", errors.New("no one-time code was entered")
			}
			return code, nil
		}
		if flagValue != "" {
			return "", fmt.Errorf("this account requires a one-time code, and the one given with --otp was "+
				"already used (a code works once). Run the command again with a fresh --otp: %w", c)
		}
		return "", fmt.Errorf("this account requires a one-time code and there is no terminal to ask on. "+
			"Pass --otp <code> (a recovery code also works): %w", c)
	}
}

// otpFlag reads --otp off a command that declares it.
func otpFlag(cmd *cobra.Command) string {
	v, _ := cmd.Flags().GetString("otp")
	return v
}
