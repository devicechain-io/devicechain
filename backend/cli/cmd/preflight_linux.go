// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// cgroup2SuperMagic identifies a cgroup v2 mount (statfs f_type).
const cgroup2SuperMagic = 0x63677270

// checkSystem runs the Linux-specific host diagnostics: cgroup version, inotify
// limits, disk headroom on the docker data-root, and WSL2 specifics.
func checkSystem(d *doctor, dockerRoot string) {
	section("Kernel & cgroups", "🧬")
	var cg syscall.Statfs_t
	if err := syscall.Statfs("/sys/fs/cgroup", &cg); err == nil {
		if int64(cg.Type) == cgroup2SuperMagic {
			d.pass("cgroup v2 (cgroup2fs)")
		} else {
			d.warn("cgroup v1 detected", "kind prefers cgroup v2; modern WSL2/distros default to it")
		}
	}
	if v := readIntFile("/proc/sys/fs/inotify/max_user_instances"); v >= 0 {
		if v >= 256 {
			d.pass(fmt.Sprintf("fs.inotify.max_user_instances = %d", v))
		} else {
			d.fail(fmt.Sprintf("fs.inotify.max_user_instances = %d (too low)", v),
				"echo 'fs.inotify.max_user_instances=512' | sudo tee /etc/sysctl.d/99-kind.conf && sudo sysctl -p /etc/sysctl.d/99-kind.conf")
		}
	}
	if v := readIntFile("/proc/sys/fs/inotify/max_user_watches"); v >= 0 {
		if v >= 524288 {
			d.pass(fmt.Sprintf("fs.inotify.max_user_watches = %d", v))
		} else {
			d.warn(fmt.Sprintf("fs.inotify.max_user_watches = %d (low)", v),
				"echo 'fs.inotify.max_user_watches=524288' | sudo tee -a /etc/sysctl.d/99-kind.conf && sudo sysctl -p /etc/sysctl.d/99-kind.conf")
		}
	}

	section("Disk", "💾")
	if dockerRoot == "" {
		dockerRoot = "/var/lib/docker"
	}
	var fs syscall.Statfs_t
	if err := syscall.Statfs(dockerRoot, &fs); err == nil {
		availGi := int64(fs.Bavail) * fs.Bsize / (1 << 30)
		if availGi >= 40 {
			d.pass(fmt.Sprintf("free on %s: %dGi", dockerRoot, availGi))
		} else {
			d.fail(fmt.Sprintf("free on %s: %dGi (low)", dockerRoot, availGi),
				"free up space; need >= 40Gi for images + DB PVCs")
		}
	} else {
		d.warn("could not read free space for "+dockerRoot, "check 'df -h "+dockerRoot+"'")
	}
	if strings.HasPrefix(dockerRoot, "/mnt/") {
		d.warn("docker data-root is on a 9p mount ("+dockerRoot+")",
			"move it to native ext4 — 9p is too slow/unsafe for database PV fsync")
	}

	if isWSL() {
		section("WSL2", "🪟")
		d.pass("running under WSL2")
		cfg := readWSLConfig()
		switch {
		case strings.Contains(cfg, "mirrored"):
			d.pass("networkingMode=mirrored (localhost shared with Windows)")
		case strings.Contains(cfg, "networkingmode"):
			d.warn("networkingMode is non-mirrored", "mirrored mode simplifies reaching the cluster from the Windows browser")
		default:
			d.warn("networkingMode not set in .wslconfig", "consider networkingMode=mirrored so the Windows browser can reach the cluster on localhost")
		}
		if strings.Contains(strings.ReplaceAll(cfg, " ", ""), "sparsevhd=true") {
			d.pass("sparseVhd=true (freed disk returns to the host)")
		} else {
			d.warn("sparseVhd not enabled", "add 'sparseVhd=true' to .wslconfig so the vhdx reclaims freed space (otherwise it only grows)")
		}
	}
}

// readIntFile reads a single integer from a /proc file; returns -1 on error.
func readIntFile(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return -1
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return -1
	}
	return n
}

// isWSL reports whether we're running under WSL (microsoft kernel marker).
func isWSL() bool {
	b, err := os.ReadFile("/proc/version")
	if err != nil {
		return false
	}
	v := strings.ToLower(string(b))
	return strings.Contains(v, "microsoft") || strings.Contains(v, "wsl")
}

// readWSLConfig returns the lowercased contents of the Windows-side .wslconfig
// (best-effort; empty if not found).
func readWSLConfig() string {
	matches, _ := filepath.Glob("/mnt/c/Users/*/.wslconfig")
	for _, m := range matches {
		if b, err := os.ReadFile(m); err == nil {
			return strings.ToLower(string(b))
		}
	}
	return ""
}
