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
// The cost, stated where it is decided: the tag is the DeviceChain release, so moving
// an instance to a new release restarts every broker server even when the broker
// binary did not change — the same outcome a chart bump has always had.
func brokerImageVars(st *State) []string {
	if st.ImageRegistry == "" || st.ImageVersion == "" {
		return nil
	}
	return []string{
		"nats_server_image_repository=" + st.ImageRegistry + "/" + brokerImageName,
		"nats_server_image_tag=" + st.ImageVersion,
	}
}
