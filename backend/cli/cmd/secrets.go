// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"github.com/devicechain-io/dc-microservice/secrets/escrow"
	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/devicechain-io/dcctl/internal/secretenv"
	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

// Escrow inspection flags.
var (
	escrowInstance             string
	escrowKubeContext          string
	escrowVerifyOpen           bool
	escrowVerifyPassphraseFile string
)

var secretsCmd = &cobra.Command{
	Use:   "secrets",
	Short: "Inspect and verify instance secret material",
}

var secretsEscrowCmd = &cobra.Command{
	Use:   "escrow",
	Short: "Work with root-key escrow artifacts",
	Long: "The instance secret-store root key wraps every stored secret. Its only copy inside the\n" +
		"cluster lives in etcd, which no DeviceChain backup contains — so `dcctl bootstrap` writes an\n" +
		"encrypted escrow artifact, and these commands are how you check that artifact is the right\n" +
		"one BEFORE the day you need it.",
}

// secretsEscrowShowCmd prints the artifact's cleartext header.
//
// Everything it prints is authenticated but unencrypted, so it needs no passphrase.
// That is the point: an operator holding three files and one memory can work out
// which file belongs to which instance without typing a passphrase into anything.
var secretsEscrowShowCmd = &cobra.Command{
	Use:   "show <file>",
	Short: "Print what an escrow artifact says about itself (no passphrase needed)",
	Long: `Print the cleartext header of a root-key escrow artifact: the instance it belongs to, when it
was created, its cipher and key-derivation parameters, and the digest of the root key it holds.

Everything printed is cleartext and needs no passphrase; nothing is decrypted. The header is
bound to the encrypted key, so an edit to it fails at restore, but show does not check that. Use it to tell which of several files belongs to which instance. To check an
artifact against a live instance, use "dcctl secrets escrow verify".`,
	Example: `  dcctl secrets escrow show ~/.devicechain/escrow/prod.escrow
  dcctl secrets escrow show ./prod-rootkey.escrow`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		art, err := readEscrowArtifact(args[0])
		if err != nil {
			return err
		}
		fmt.Printf("  %s %s\n", color.WhiteString("File:       "), color.GreenString(args[0]))
		fmt.Printf("  %s %s\n", color.WhiteString("Instance:   "), color.GreenString(art.Instance))
		fmt.Printf("  %s %s\n", color.WhiteString("Created:    "), color.GreenString(art.Created.Format("2006-01-02 15:04:05 MST")))
		fmt.Printf("  %s %s\n", color.WhiteString("Cipher:     "), color.GreenString(art.Cipher))
		fmt.Printf("  %s %s\n", color.WhiteString("KDF:        "), color.GreenString(
			"%s (t=%d, m=%d KiB, p=%d)", art.KDF.Alg, art.KDF.Time, art.KDF.Memory, art.KDF.Threads))
		fmt.Printf("  %s %s\n", color.WhiteString("Key digest: "), color.GreenString(art.Fingerprint))
		return nil
	},
	SilenceUsage: true,
}

// secretsEscrowVerifyCmd compares the header by default for compatibility. --open
// additionally authenticates the artifact and compares the recovered key bytes.
var secretsEscrowVerifyCmd = &cobra.Command{
	Use:   "verify <file>",
	Short: "Compare an escrow header with a live instance, or open it with --open",
	Long: `Compare a root-key escrow artifact with the root key a live instance is running.

By default, compare only the cleartext header fingerprint with the key in the instance's
configuration Secret (read through --kube-context, or the current context). This needs no
passphrase and decrypts nothing. A matching header does not prove the artifact can open.

With --open, decrypt and authenticate the artifact and compare the recovered key bytes
with the running key. Supply the original passphrase using --escrow-passphrase-file or
DCCTL_ESCROW_PASSPHRASE (the file takes precedence). This mode never prompts. A missing or
wrong passphrase, damaged artifact, or different key exits non-zero.

Without --open, a fingerprint mismatch also exits non-zero: the header names a different
key, usually because the instance was re-bootstrapped or the file belongs to another instance.`,
	Example: `  # compare the header only; this does not check recoverability
  dcctl secrets escrow verify --instance prod ./prod-rootkey.escrow
  # prove that the artifact opens to the key the instance is running
  dcctl secrets escrow verify --instance prod --open --escrow-passphrase-file ./passphrase ./prod-rootkey.escrow
  dcctl secrets escrow verify --instance prod --open --kube-context prod ./prod-rootkey.escrow`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if escrowInstance == "" {
			return fmt.Errorf("--instance is required: the check is against a live instance's config Secret")
		}
		art, err := readEscrowArtifact(args[0])
		if err != nil {
			return err
		}
		var pass string
		if escrowVerifyOpen {
			pass, err = escrowVerificationPassphrase(escrowVerifyPassphraseFile)
			if err != nil {
				return err
			}
		}
		key, err := bootstrap.DeployedRootKey(cmd.Context(), escrowKubeContext, escrowInstance)
		if err != nil {
			return err
		}
		defer clear(key)
		if escrowVerifyOpen {
			opened, err := escrow.Open(art, pass)
			if err != nil {
				return fmt.Errorf("%s: %w", args[0], err)
			}
			defer clear(opened)
			if !bytes.Equal(opened, key) {
				return fmt.Errorf("%s opens to a different root key than instance %q is running", args[0], escrowInstance)
			}
			fmt.Println(color.HiGreenString("%s opened to the root key instance %q is running.", args[0], escrowInstance))
		} else {
			if !art.Matches(key) {
				return fmt.Errorf("%s names a different root key than instance %q is running; this instance has no confirmed usable escrow", args[0], escrowInstance)
			}
			fmt.Println(color.HiGreenString("%s names the root key instance %q is running (header only; use --open to check it opens).", args[0], escrowInstance))
		}
		fmt.Printf("  %s %s\n", color.WhiteString("Key digest:"), color.GreenString(art.Fingerprint))
		return nil
	},
	SilenceUsage: true,
}

// escrowVerificationPassphrase never prompts: an unattended verification must fail
// when it cannot authenticate the artifact. Read only through the captured secret
// environment so Kubernetes exec plugins never inherit the passphrase.
func escrowVerificationPassphrase(file string) (string, error) {
	if file != "" {
		raw, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("reading the escrow passphrase file: %w", err)
		}
		pass := strings.TrimRight(string(raw), "\r\n")
		if pass == "" {
			return "", fmt.Errorf("the escrow passphrase file %s is empty", file)
		}
		return pass, nil
	}
	if pass, ok := secretenv.LookupEscrowPassphrase(); ok && pass != "" {
		return pass, nil
	}
	return "", fmt.Errorf("--open requires --escrow-passphrase-file or a non-empty %s", secretenv.EscrowPassphrase)
}

// readEscrowArtifact loads and parses an artifact, reporting the path in the error
// so a mistyped file in a scripted check says which file it was.
func readEscrowArtifact(path string) (*escrow.Artifact, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading the escrow artifact: %w", err)
	}
	art, err := escrow.Decode(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return art, nil
}

func init() {
	secretsEscrowVerifyCmd.Flags().BoolVar(&escrowVerifyOpen, "open", false, "decrypt the artifact and compare its key bytes with the live instance (requires a passphrase)")
	secretsEscrowVerifyCmd.Flags().StringVar(&escrowVerifyPassphraseFile, "escrow-passphrase-file", "", "read the original passphrase for --open from this file (or $"+secretenv.EscrowPassphrase+")")
	secretsEscrowVerifyCmd.Flags().StringVar(&escrowInstance, "instance", "", "instance id to check the artifact against (required)")
	secretsEscrowVerifyCmd.Flags().StringVar(&escrowKubeContext, "kube-context", "", "kube-context to target (default: current)")

	secretsEscrowCmd.AddCommand(secretsEscrowShowCmd)
	secretsEscrowCmd.AddCommand(secretsEscrowVerifyCmd)
	secretsCmd.AddCommand(secretsEscrowCmd)
	rootCmd.AddCommand(secretsCmd)
}
