// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/devicechain-io/dcctl/internal/installchoice"
)

// Adapters for the existing focused option tests. Production resolves typed
// choices directly; these helpers keep those regression tests exercising the
// same shared implementation without restoring a second option builder.
var installMQTTGatewayGiven bool

func installRestoreFlagsFromArgv(backupsEnabled bool) bootstrap.RestoreFlags {
	return bootstrap.RestoreFlags{
		RdbFrom:        installRestoreRdbFrom,
		RdbTargetTime:  installRestoreRdbAt,
		BackupsEnabled: backupsEnabled,
	}
}

func installPlacementFromArgv() (bootstrap.DatabasePlacement, error) {
	return bootstrap.ParseDatabasePlacement(installDBNodeSelector, installDBTolerations)
}

func resolveInstallDevMode(changed func(string) bool, build bool, version string) (installDevResolution, error) {
	r, err := installchoice.ResolveDev(changed, build, version)
	return installDevResolution{Build: r.Build, Yes: r.Yes}, err
}

type installDevResolution struct {
	Build bool
	Yes   bool
}

type compactModeResolution struct {
	NoTLS        bool
	NoMonitoring bool
}

func resolveCompactMode(changed func(string) bool, noTLS, noMonitoring bool) compactModeResolution {
	r := installchoice.ResolveCompact(changed, noTLS, noMonitoring)
	return compactModeResolution{NoTLS: r.NoTLS, NoMonitoring: r.NoMonitoring}
}

func installOptions(dest *bootstrap.BackupDestination, restore bootstrap.RestorePlan,
	img bootstrap.ImageSource) (bootstrap.InstallOptions, error) {
	c, err := installChoices(installCmd)
	if err != nil {
		return bootstrap.InstallOptions{}, err
	}
	// Existing focused option tests set these command-layer presence seams directly.
	c.Given["mqtt-gateway"] = installMQTTGatewayGiven
	return c.Options(dest, restore, img, Version)
}

func resolveInstallSize(compact, compactGiven bool, size string) (bool, error) {
	return installchoice.ResolveSize(compact, compactGiven, size)
}
