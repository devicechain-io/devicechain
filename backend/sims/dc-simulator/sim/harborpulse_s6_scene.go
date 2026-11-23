// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import "fmt"

// harborS6SceneDevices are the S6 devices as the scene draws them, after the district's own in
// HarborpulseSceneLayout. Each population has its own scene kind: the scene binds one device
// type per kind and reads that kind's own metrics, and these report less than the district's
// chargers and street lights do (a charging power, a dim level), so they are not drawn as either.
// The manifest gives none of them a surveyed position; the scene places them on North Quay,
// the area the manifest assigns them, and numbers each pair from one.
func harborS6SceneDevices() []HarborSceneDev {
	var out []HarborSceneDev
	for _, group := range []struct {
		typ, kind string
		tokens    []string
	}{
		{HarborpulseMQTTChargerType, harborSceneMQTTCharger, HarborpulseMQTTChargers()},
		{HarborpulseX509ChargerType, harborSceneCertCharger, HarborpulseX509Chargers()},
		{HarborpulsePullLightType, harborScenePullLight, HarborpulsePullLights()},
	} {
		for i, token := range group.tokens {
			ext := token
			if group.typ == HarborpulseX509ChargerType {
				ext = fmt.Sprintf("%spin-%04d", harborX509Prefix, i+1)
			}
			out = append(out, HarborSceneDev{Token: token, ExternalId: ext, DeviceType: group.typ, Kind: group.kind, Quay: HarborpulseNorthQuay, Index: i + 1})
		}
	}
	return out
}
