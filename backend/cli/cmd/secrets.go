// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"
	"os"

	"github.com/devicechain-io/dc-microservice/secrets/escrow"
	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

// Escrow inspection flags.
var (
	escrowInstance    string
	escrowKubeContext string
)

var secretsCmd = &cobra.Command{
	Use:   "secrets",
	Short: "Inspect and verify instance secret material",
}

var secretsEscrowCmd = &cobra.Command{
	Use:   "escrow",
	Short: "Work with root-key escrow artifacts (ADR-059)",
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
	Args:  cobra.ExactArgs(1),
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

// secretsEscrowVerifyCmd answers the only question that matters about an escrow
// artifact: does it hold the key this instance is actually running?
//
// It compares FINGERPRINTS, so it needs no passphrase and never decrypts anything —
// which is what makes it a check an operator can run on a Tuesday, as often as they
// like, without handling the passphrase at all. The failure it catches is an escrow
// that was silently orphaned: a re-bootstrap that minted a new key, a hand-edited
// Secret, an artifact copied from the wrong instance. Every one of those looks
// exactly like a healthy setup until a restore, which is far too late to find out.
//
// A mismatch exits non-zero so this composes into a cron job or a CI gate.
var secretsEscrowVerifyCmd = &cobra.Command{
	Use:   "verify <file>",
	Short: "Check that an escrow artifact holds the root key a live instance is running",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if escrowInstance == "" {
			return fmt.Errorf("--instance is required: the check is against a live instance's config Secret")
		}
		art, err := readEscrowArtifact(args[0])
		if err != nil {
			return err
		}
		key, err := bootstrap.DeployedRootKey(cmd.Context(), escrowKubeContext, escrowInstance)
		if err != nil {
			return err
		}
		if !art.Matches(key) {
			// Named as an orphan rather than as a corrupt file: the artifact is
			// almost certainly intact and simply describes a key this instance no
			// longer uses, and telling an operator their backup is "invalid" would
			// send them to delete the wrong thing.
			return fmt.Errorf(
				"%s does NOT hold the root key instance %q is running.\n"+
					"  The artifact is intact — it just belongs to a different key, so it would not open this\n"+
					"  instance's stored secrets. Usually that means the instance was re-bootstrapped (minting a\n"+
					"  fresh key) after this file was written, or the file belongs to another instance.\n"+
					"  Until this is resolved, this instance has NO usable escrow.",
				args[0], escrowInstance)
		}
		fmt.Println(color.HiGreenString("%s holds the root key instance %q is running.", args[0], escrowInstance))
		fmt.Printf("  %s %s\n", color.WhiteString("Key digest:"), color.GreenString(art.Fingerprint))
		return nil
	},
	SilenceUsage: true,
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
	secretsEscrowVerifyCmd.Flags().StringVar(&escrowInstance, "instance", "", "instance id to check the artifact against (required)")
	secretsEscrowVerifyCmd.Flags().StringVar(&escrowKubeContext, "kube-context", "", "kube-context to target (default: current)")

	secretsEscrowCmd.AddCommand(secretsEscrowShowCmd)
	secretsEscrowCmd.AddCommand(secretsEscrowVerifyCmd)
	secretsCmd.AddCommand(secretsEscrowCmd)
	rootCmd.AddCommand(secretsCmd)
}
