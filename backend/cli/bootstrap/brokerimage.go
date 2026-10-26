// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

const (
	// brokerModuleDir is where DeviceChain's broker build lives, relative to the repository
	// root, and brokerImageName what the infrastructure's nats module pulls it as
	// ({registry}/dc-nats-server:{tag}). It is upstream nats-server's own main on the
	// unmodified library, plus the MQTT listener requesting (not requiring) a client
	// certificate; see backend/broker/dc-nats-server.
	brokerModuleDir = "backend/broker/dc-nats-server"
	brokerImageName = "dc-nats-server"
)

// brokerBuildEnv is how the broker image is built everywhere it is built: from the module's own
// go.mod (GOWORK=off: in workspace mode it would link the versions the whole workspace selects,
// so a bump in an unrelated module would change it) and with no VCS stamp (-buildvcs=false: the
// revision would make every commit a new binary). The same source then gives the same image, so a
// release that did not change the broker does not roll it. The release, CI and
// deploy/local/build-images.sh set the same two; backend/broker/dc-nats-server/build_paths_test.go
// checks them all.
var brokerBuildEnv = []string{"GOWORK=off", "GOFLAGS=-buildvcs=false"}

// DefaultBrokerImageDigest is the digest of the broker image the release that built this dcctl
// pushed (sha256:<64 hex>), stamped by goreleaser from that push's own record (release.yml, the
// cli job, from the images job's ref-dc-nats-server artifact). Empty in every other build.
//
// 🔑 IT IS WHAT KEEPS AN UNCHANGED BROKER FROM ROLLING. The broker image is reproducible (see
// brokerBuildEnv), so a release that changed nothing under the broker publishes the SAME digest
// under a new tag. The chart pulls repository@digest when a digest is given, so the pod template
// does not move and the servers are not restarted; by tag alone, every release would restart
// them.
var DefaultBrokerImageDigest = ""

// brokerImageVars are the instance root's broker image variables: the same registry and
// release tag every service image is pulled at, so a bootstrap, an upgrade and a
// --build run all put the broker on the build of the release they deploy.
//
// 🔴 NOTHING IS EMITTED FOR AN UNRESOLVED IMAGE SOURCE, rather than a half-made one.
// Every path that applies the instance root resolves it first (ResolveImageSource), so
// an empty registry or version here is a defect upstream of this function; passing
// "nats_server_image_repository=/dc-nats-server" would be refused by the root's
// validation with a message about the repository's spelling, and an empty tag would be
// refused by the module as missing. Emitting nothing leaves the module's own refusal —
// "nats server_image_tag ... is required" — which names what is actually absent.
//
// The tag is the DeviceChain release. On its own it would restart every broker server on every
// release; the digest (brokerImageDigest) is what lets a release that did not change the broker
// leave it running.
func brokerImageVars(st *State) []string {
	if st.ImageRegistry == "" || st.ImageVersion == "" {
		return nil
	}
	vars := []string{
		"nats_server_image_repository=" + st.ImageRegistry + "/" + brokerImageName,
		"nats_server_image_tag=" + st.ImageVersion,
	}
	if d := brokerImageDigest(st); d != "" {
		vars = append(vars, "nats_server_image_digest="+d)
	}
	return vars
}

// brokerImageDigest is the digest to pin the broker by, or "" to pull it by tag.
//
// Only the image this dcctl's own release pushed has a digest dcctl knows: the published registry
// at the release's own tag. Any other source — another registry, another --version, a --build —
// is pulled by tag, because the stamped digest would name a different image. A malformed stamp is
// passed through rather than dropped, so the instance root's validation refuses it by name: it is
// a defect in the release tooling, and pulling by tag instead would hide it behind a working
// install.
func brokerImageDigest(st *State) string {
	if DefaultBrokerImageDigest == "" || st.BuildImages ||
		st.ImageRegistry != DefaultImageRegistry || st.ImageVersion != DefaultImageVersion {
		return ""
	}
	return DefaultBrokerImageDigest
}
