// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import "fmt"

// harborS6SceneDevices are the S6 devices as the layout generator WOULD place them. They are
// deliberately NOT in HarborpulseSceneLayout: the Unity scene authors one presentation anchor per
// layout device and refuses any other count, so the committed scene contract stays the 37 devices
// the district draws. The verifier's fleet (HarborpulseFleet) and the slot test use these.
func harborS6SceneDevices() []HarborSceneDev {
	var out []HarborSceneDev
	for _, group := range []struct {
		typ, kind string
		tokens    []string
	}{
		{HarborpulseMQTTChargerType, harborSceneCharger, HarborpulseMQTTChargers()},
		{HarborpulseX509ChargerType, harborSceneCharger, HarborpulseX509Chargers()},
		{HarborpulsePullLightType, harborSceneLight, HarborpulsePullLights()},
	} {
		for i, token := range group.tokens {
			ext := token
			if group.typ == HarborpulseX509ChargerType {
				ext = fmt.Sprintf("%spin-%04d", harborX509Prefix, i+1)
			}
			offset := 4
			if group.typ == HarborpulseMQTTChargerType {
				offset = 2
			}
			out = append(out, HarborSceneDev{Token: token, ExternalId: ext, DeviceType: group.typ, Kind: group.kind, Quay: HarborpulseNorthQuay, Index: offset + i + 1})
		}
	}
	return out
}
