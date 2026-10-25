// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/selfreg"
	"github.com/devicechain-io/dcctl/sim"
	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

// provisionSecretEnv names the environment variable the provision secret may be read from, so
// it need not appear in a process listing or a shell history.
const provisionSecretEnv = "DCCTL_PROVISION_SECRET"

// deviceCmd is the parent for commands that act AS a device.
var deviceCmd = &cobra.Command{
	Use:   "device",
	Short: "Act as a device",
	Long: `Commands that play a device against the platform, from a workstation.

They exist to try a flow end to end before firmware does it, and to enroll a device by hand;
they are not how an operator manages devices (that is the console and the API).`,
}

func init() {
	deviceCmd.AddCommand(newDeviceRegisterCmd())
	rootCmd.AddCommand(deviceCmd)
}

// deviceRegisterFlags are one run's flags. The command is built by a function so a test gets
// its own flag set, and no flag value outlives a run.
type deviceRegisterFlags struct {
	server       string
	insecure     bool
	tenant       string
	key          string
	secret       string
	secretFile   string
	externalId   string
	materialFile string
	name         string
	maxAttempts  int
	timeout      time.Duration
}

func newDeviceRegisterCmd() *cobra.Command {
	var f deviceRegisterFlags
	c := &cobra.Command{
		Use:   "register",
		Short: "Register a device through a provisioning profile, as the device would",
		Long: `Play a device registering itself: present a provisioning profile's key and secret
with a serial number, and receive the device's token and MQTT client id.

The device makes its own credential. This command generates it (32 random bytes, base64url) and
SAVES IT TO --material-file BEFORE SENDING anything, so a retry, a crash or a second run all
present the same credential and a registration whose answer was lost is recognised as a replay,
not refused as a second device. An existing file is reused, never overwritten. The platform keeps
only a digest of the credential, so the file is the only copy: keep it with the device.

The credential is the MQTT username "<tenant>:<credential>" with no password. It is printed
once, on the run that registered the device; running the command again prints the device's
details (outcome REPLAYED) and not the credential.

It follows the endpoint's client contract. On 429 and 503 it waits the Retry-After the platform
names plus a random jitter of up to 100% of it. A network error or a 5xx the platform may
recover from backs off from 1 second to 5 minutes with full jitter. Refusals that need a person
(400, 401, 403, 404, 409, 413, 422, 501, a misconfigured profile) are not retried.

The provision secret is read from --secret, from --secret-file, or from the environment
variable ` + provisionSecretEnv + `. Give at most one of the two flags (both is an error); either
wins over the variable. Prefer the file or the variable, which keep it out of the process
listing.

The request carries the provision secret, so it always goes over https. --insecure-plaintext
sends it over plaintext http instead, which exposes the secret to anyone on the path; it exists
for a local test cluster without a certificate. A redirect from the server is never followed
(a plain-http ingress that answers 308 would otherwise make the secret travel twice), and is
reported as an error.

The endpoint needs the instance's API ingress, and --server is that ingress host exactly as for
the other commands.`,
		Example: `  dcctl device register --server dc.example.com --tenant acme \
      --key acme-sensors-2026 --secret-file ./provision.secret \
      --external-id SN-000123 --material-file ./SN-000123.credential`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDeviceRegister(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), &f)
		},
	}
	fl := c.Flags()
	fl.StringVarP(&f.server, "server", "s", "localhost", "instance host for platform API calls")
	fl.BoolVar(&f.insecure, "insecure-plaintext", false, "send the request over plaintext http, secret included; for a local test cluster only")
	fl.StringVarP(&f.tenant, "tenant", "t", "", "tenant the device registers into (required)")
	fl.StringVar(&f.key, "key", "", "the provisioning profile's provision key (required)")
	fl.StringVar(&f.secret, "secret", "", "the provisioning profile's provision secret; prefer --secret-file or $"+provisionSecretEnv)
	fl.StringVar(&f.secretFile, "secret-file", "", "file holding the provision secret")
	fl.StringVar(&f.externalId, "external-id", "", "the device's serial number or other external id (required)")
	fl.StringVar(&f.materialFile, "material-file", "", "file the device's credential is saved to before it is sent, and read back on a re-run (required)")
	fl.StringVar(&f.name, "name", "", "optional device name, used only when the registration creates the device")
	fl.IntVar(&f.maxAttempts, "max-attempts", 10, "give up after this many sends (0 keeps trying until --timeout)")
	fl.DurationVar(&f.timeout, "timeout", 10*time.Minute, "give up after this long overall (0 for no limit)")
	return c
}

// resolveProvisionSecret picks the secret from its three sources and refuses an ambiguous or
// empty answer.
func resolveProvisionSecret(f *deviceRegisterFlags) (string, error) {
	set := 0
	for _, v := range []string{f.secret, f.secretFile} {
		if v != "" {
			set++
		}
	}
	if set > 1 {
		return "", errors.New("give the provision secret once: --secret and --secret-file are both set")
	}
	switch {
	case f.secret != "":
		return f.secret, nil
	case f.secretFile != "":
		raw, err := os.ReadFile(f.secretFile)
		if err != nil {
			return "", fmt.Errorf("read --secret-file: %w", err)
		}
		s := strings.TrimSpace(string(raw))
		if s == "" {
			return "", fmt.Errorf("--secret-file %s is empty", f.secretFile)
		}
		return s, nil
	}
	if s := strings.TrimSpace(os.Getenv(provisionSecretEnv)); s != "" {
		return s, nil
	}
	return "", fmt.Errorf("the provision secret is required: --secret, --secret-file or $%s", provisionSecretEnv)
}

func runDeviceRegister(ctx context.Context, out, errOut io.Writer, f *deviceRegisterFlags) error {
	for name, v := range map[string]string{"--tenant": f.tenant, "--key": f.key, "--external-id": f.externalId, "--material-file": f.materialFile} {
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	secret, err := resolveProvisionSecret(f)
	if err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if f.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, f.timeout)
		defer cancel()
	}

	res, err := selfreg.Register(ctx, selfreg.Request{
		Tenant: f.tenant, Key: f.key, Secret: secret, ExternalId: f.externalId, Name: f.name,
	}, selfreg.Options{
		BaseURL:        registerBaseURL(f),
		AllowPlaintext: f.insecure,
		MaterialFile:   f.materialFile,
		MaxAttempts:    f.maxAttempts,
		Logf:           func(format string, args ...any) { fmt.Fprintf(errOut, format+"\n", args...) },
	})
	if err != nil {
		var apiErr *selfreg.APIError
		if errors.As(err, &apiErr) {
			return fmt.Errorf("%w\n(the credential saved in %s is unchanged and is presented again on a re-run)", err, f.materialFile)
		}
		return err
	}
	printRegistration(out, res)
	return nil
}

// registerBaseURL is https unless the caller asked for plaintext by name.
func registerBaseURL(f *deviceRegisterFlags) string {
	return sim.DeviceManagementURL(f.server, !f.insecure)
}

// printRegistration writes the outcome. The credential is printed only on the run that
// created it AND registered a new device or adopted one: a replay, or a run that found the
// material already on disk, points at the file instead, so the secret is not repeated on
// every run.
func printRegistration(out io.Writer, r *selfreg.Result) {
	fmt.Fprintln(out, color.GreenString("Registered: %s", r.Outcome))
	fmt.Fprintf(out, "  tenant          %s\n", r.Tenant)
	fmt.Fprintf(out, "  device token    %s\n", r.DeviceToken)
	fmt.Fprintf(out, "  external id     %s\n", r.ExternalId)
	fmt.Fprintf(out, "  MQTT client id  %s\n", r.ClientId)
	fmt.Fprintf(out, "  MQTT username   %s:<credential>   (no password)\n", r.Tenant)
	if r.MaterialCreated && r.Outcome != "REPLAYED" {
		fmt.Fprintf(out, "  credential      %s\n", r.Material)
		fmt.Fprintf(out, "\nThis is the only time the credential is printed. The platform keeps a digest of it, not the\n"+
			"credential; the one copy is %s.\n", r.MaterialFile)
		return
	}
	fmt.Fprintf(out, "\nThe credential is in %s (it is not printed again).\n", r.MaterialFile)
}
