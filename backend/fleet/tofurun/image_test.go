// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package tofurun

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

var (
	argRe    = regexp.MustCompile(`(?m)^ARG ([A-Z0-9_]+)=(\S*)\s*$`)
	fromRe   = regexp.MustCompile(`(?m)^FROM\s+(\S+)(?:\s+AS\s+\S+)?\s*$`)
	sha64    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	pinnedRe = regexp.MustCompile(`@sha256:[0-9a-f]{64}$`)
)

// problems lists what is wrong with the runner image definition. The image cannot be built in a
// unit test, so its load-bearing properties are checked as text, and the checker is itself shown to
// refuse a defective copy of the real file.
func problems(df string) []string {
	var out []string
	args := map[string]string{}
	for _, m := range argRe.FindAllStringSubmatch(df, -1) {
		args[m[1]] = m[2]
	}
	if args["TOFU_VERSION"] != PinnedVersion {
		out = append(out, "TOFU_VERSION is not tofurun.PinnedVersion")
	}
	for _, k := range []string{"TOFU_SHA256_AMD64", "TOFU_SHA256_ARM64"} {
		if !sha64.MatchString(args[k]) {
			out = append(out, k+" is not a sha256")
		}
	}
	if !strings.Contains(df, "sha256sum -c") {
		out = append(out, "the tofu archive is not verified")
	}
	froms := fromRe.FindAllStringSubmatch(df, -1)
	if len(froms) < 2 {
		out = append(out, "expected a build stage and a final stage")
	}
	for _, f := range froms {
		if !pinnedRe.MatchString(f[1]) {
			out = append(out, "base image "+f[1]+" is not pinned by digest")
		}
	}
	if len(froms) > 0 {
		final := df[strings.LastIndex(df, "FROM "):]
		if strings.Contains(final, "apt-get") || strings.Contains(final, "curl") {
			out = append(out, "the final stage installs or downloads")
		}
		if !strings.Contains(final, "USER 65532") {
			out = append(out, "the final stage does not run as a non-root user")
		}
		if !strings.Contains(final, `ENTRYPOINT ["/usr/local/bin/fleet-runner"]`) {
			out = append(out, "the entrypoint is not fleet-runner")
		}
		if !strings.Contains(final, "gcr.io/distroless/static") {
			out = append(out, "the final stage is not distroless static")
		}
	}
	for _, banned := range []string{"install terraform", "terraform_", "/terraform", "gcloud", "kubectl", "google-cloud-cli"} {
		for _, line := range strings.Split(df, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			if strings.Contains(line, banned) {
				out = append(out, "the image mentions "+banned)
			}
		}
	}
	if !strings.Contains(df, "-lockfile=readonly") || strings.Contains(df, "providers lock") {
		out = append(out, "the build does not install under the committed lock files (init -lockfile=readonly)")
	}
	if !strings.Contains(df, "fleet-runner check") {
		out = append(out, "the build does not run the layout check")
	}
	return out
}

func TestRunnerImageDefinition(t *testing.T) {
	b, err := os.ReadFile("image/Dockerfile")
	require.NoError(t, err)
	df := string(b)
	require.Empty(t, problems(df))

	// The checker can fail: each defect, planted in a copy of the real file, is seen.
	mutate := map[string]string{
		"a version that is not the pin":        strings.Replace(df, "ARG TOFU_VERSION="+PinnedVersion, "ARG TOFU_VERSION=1.9.0", 1),
		"no archive verification":              strings.Replace(df, "sha256sum -c -", "true", 1),
		"a malformed checksum":                 strings.Replace(df, "ARG TOFU_SHA256_AMD64=", "ARG TOFU_SHA256_AMD64=xx", 1),
		"an unpinned base":                     strings.Replace(df, "FROM gcr.io/distroless/static-debian12:nonroot@sha256:", "FROM gcr.io/distroless/static-debian12:nonroot #", 1),
		"a root final stage":                   strings.Replace(df, "USER 65532:65532", "USER 0", 1),
		"a terraform binary":                   df + "\nRUN apt-get install -y terraform\n",
		"a downloading final stage":            df + "\nRUN curl -fsSL https://example.invalid/x\n",
		"a different entrypoint":               strings.Replace(df, `ENTRYPOINT ["/usr/local/bin/fleet-runner"]`, `ENTRYPOINT ["/usr/local/bin/tofu"]`, 1),
		"a build that never checks":            strings.ReplaceAll(df, "fleet-runner check", "fleet-runner noop"),
		"a build that rewrites the lock files": strings.ReplaceAll(df, "-lockfile=readonly", "-upgrade"),
		"a gcloud in the image":                df + "\nCOPY --from=x /gcloud /usr/bin/gcloud\n",
		"a kubectl in the image":               df + "\nCOPY --from=x /kubectl /usr/bin/kubectl\n",
		"a final stage that is not slim":       strings.Replace(df, "gcr.io/distroless/static-debian12", "debian", 1),
	}
	for name, bad := range mutate {
		require.NotEqual(t, df, bad, name+": the mutation must change the file")
		require.NotEmpty(t, problems(bad), name)
	}
}
