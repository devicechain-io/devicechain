// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import "fmt"

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

// HarborpulseSceneLayout includes the additive S6 population and cues. The Unity lane owns
// adopting this generated contract in its Resources file; this lane never rewrites that file.
func HarborpulseSceneLayout() HarborSceneLayout {
	out := harborpulseBaseSceneLayout()
	out.Devices = append(out.Devices, harborS6SceneDevices()...)
	out.Cues.MQTTDropFromSim, out.Cues.MQTTDropToSim = HarborpulseMQTTDropFrom, HarborpulseMQTTDropTo
	out.Cues.X509RevokeSim, out.Cues.ConfigPushSim, out.Cues.PullCommandSim = HarborpulseX509RevokeHour, HarborpulseConfigHour, HarborpulsePullHour
	return out
}
