// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/devicechain-io/dcctl/bootstrap"
)

func TestInstallReadsAlertFilesOnlyAfterPrintedSuccessfulPreflight(t *testing.T) {
	for _, flag := range []string{"alert-config", "alert-smtp-password-file"} {
		for _, mode := range []string{"pass", "fail", "skip"} {
			t.Run(flag+"/"+mode, func(t *testing.T) {
				oldLook, oldRun, oldPort, oldSystem := lookPath, runCmd, portInUse, systemCheck
				oldRead, oldEngine, oldVersion := readAlertFile, installEngine, bootstrap.DefaultImageVersion
				oldAlerts := installAlerts
				t.Cleanup(func() {
					lookPath, runCmd, portInUse, systemCheck = oldLook, oldRun, oldPort, oldSystem
					readAlertFile, installEngine, bootstrap.DefaultImageVersion = oldRead, oldEngine, oldVersion
					installAlerts = oldAlerts
				})
				fakeGoldenHost(installScenario{})
				writeKubeconfig(t)
				bootstrap.DefaultImageVersion = "v1.9.0"
				for _, name := range []string{"skip-preflight", "compact", "size", "build", "kube-context", "no-tls", "no-monitoring", flag} {
					f := installCmd.Flags().Lookup(name)
					value, changed := f.Value.String(), f.Changed
					t.Cleanup(func() {
						if err := f.Value.Set(value); err != nil {
							t.Error(err)
						}
						f.Changed = changed
					})
				}
				installAlerts = alertFlags{}
				argv := []string{"--compact=false", "--size=", "--build=false", "--kube-context=", "--no-tls=false", "--no-monitoring=false",
					fmt.Sprintf("--skip-preflight=%v", mode == "skip"), "--" + flag + "=alert-file"}
				if err := installCmd.Flags().Parse(argv); err != nil {
					t.Fatal(err)
				}
				if flag == "alert-smtp-password-file" {
					installAlerts.email = []string{"ops@example.test"}
					installAlerts.smtpHost, installAlerts.smtpFrom, installAlerts.smtpUser = "smtp.example.test:587", "dc@example.test", "mailer"
				}
				if mode == "fail" {
					systemCheck = func(d *doctor, _ string, _ bool) { d.fails++ }
				}
				reads, clusters := 0, 0
				readAlertFile = func(string) ([]byte, error) {
					reads++
					printed, err := os.ReadFile(os.Stdout.Name())
					if err != nil {
						t.Fatal(err)
					}
					if mode != "skip" && !strings.Contains(string(printed), "Preflight: all checks passed.") {
						t.Error("alert file read before the successful preflight was printed")
					}
					if flag == "alert-config" {
						return []byte("route: {receiver: r}\nreceivers: [{name: r}]\n"), nil
					}
					return []byte("test-password\n"), nil
				}
				installEngine = func(_ context.Context, _ bootstrap.Provider, opts bootstrap.InstallOptions) error {
					clusters++
					if !opts.AlertRouting.Given() {
						t.Error("resolved alert request did not reach the cluster work")
					}
					return nil
				}
				var runErr error
				output := capacityOutput(t, func() { runErr = installCmd.RunE(installCmd, []string{"local"}) })
				if mode == "fail" {
					if reads != 0 || clusters != 0 || runErr == nil || !strings.Contains(runErr.Error(), "preflight check(s) failed") {
						t.Errorf("failed preflight: reads %d, cluster calls %d, error %v", reads, clusters, runErr)
					}
				} else if reads != 1 || clusters != 1 || runErr != nil {
					t.Errorf("reads %d, cluster calls %d, error %v\n%s", reads, clusters, runErr, output)
				}
			})
		}
	}
}
