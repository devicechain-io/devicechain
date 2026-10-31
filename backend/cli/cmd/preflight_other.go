// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// checkSystem runs the host diagnostics that are meaningful off Linux. The kernel,
// cgroup and WSL checks rely on Linux /proc and statfs semantics and are skipped,
// but disk headroom is not: on Windows and macOS the Docker data root lives inside
// Docker Desktop's VM, so the check is the host volume that holds the VM disk image
// and dcctl's own state, plus whatever Docker itself will report.
func checkSystem(d *doctor, _ string, _ bool) {
	section("System", "🖥️")
	d.info("kernel/cgroup/WSL diagnostics skipped on a non-Linux host (they apply to the Linux engine)")
	checkHostDisk(d, hostStatePath(), freeBytes)
	checkDockerDiskUsage(d)
}

// hostStatePath is the directory dcctl keeps its state under (or its home, when
// that does not exist yet), which is also a fair proxy for the volume Docker
// Desktop's disk image sits on.
func hostStatePath() string {
	if dir, err := os.UserConfigDir(); err == nil {
		return dir
	}
	if dir, err := os.UserHomeDir(); err == nil {
		return dir
	}
	return os.TempDir()
}

// Thresholds mirror the Linux data-root check: 40Gi is what images plus database
// volumes want. Below it is a warning here (the VM disk is sparse and the host
// volume is only a proxy for it); well below is a failure.
const (
	hostDiskWarnGi = 40
	hostDiskFailGi = 20
)

// checkHostDisk reports free space on the volume holding path. free is a seam so a
// test can present any amount without a disk.
func checkHostDisk(d *doctor, path string, free func(string) (uint64, error)) {
	section("Disk", "💾")
	probe := path
	for probe != "" {
		if _, err := os.Stat(probe); err == nil {
			break
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			break
		}
		probe = parent
	}
	b, err := free(probe)
	if err != nil {
		d.warn("could not read free space for "+path, "check free disk space manually; the install needs >= 40Gi for images + database volumes")
		return
	}
	gi := int64(b / (1 << 30))
	switch {
	case gi >= hostDiskWarnGi:
		d.pass(fmt.Sprintf("host free on %s: %dGi", probe, gi))
	case gi >= hostDiskFailGi:
		d.warn(fmt.Sprintf("host free on %s: %dGi (low)", probe, gi),
			"free up space; need >= 40Gi for images + DB volumes (Docker Desktop's disk image grows on this volume)")
	default:
		d.fail(fmt.Sprintf("host free on %s: %dGi (low)", probe, gi),
			"free up space; need >= 40Gi for images + DB volumes (Docker Desktop's disk image grows on this volume)")
	}
}

// checkDockerDiskUsage reports what Docker says it is already holding. Docker does
// not expose the free space inside Docker Desktop's VM, so this is information, and
// the fix text points at the setting that bounds it.
func checkDockerDiskUsage(d *doctor) {
	out := runCmd("docker", "system", "df", "--format", "{{.Type}}: {{.Size}} ({{.Reclaimable}} reclaimable)")
	if out == "" {
		d.warn("could not read Docker disk usage", "check Docker Desktop > Settings > Resources > Virtual disk limit; the install needs ~40Gi free inside it")
		return
	}
	d.info("docker disk usage: " + strings.ReplaceAll(out, "\n", "; "))
	d.info("Docker Desktop's virtual disk limit is not readable from the CLI; confirm >= 40Gi is free in Settings > Resources")
}
