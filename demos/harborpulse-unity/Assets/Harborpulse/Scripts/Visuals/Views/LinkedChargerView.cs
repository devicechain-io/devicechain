// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using DeviceChain.Harborpulse.Model;
using UnityEngine;

namespace DeviceChain.Harborpulse.Visuals
{
    /// <summary>
    /// A charger that holds its own session, as its board has it. The lamp on its hood is lit in its own colour while it reports (cyan on
    /// a token, gold on a certificate), violet once it has been silent past the platform's window, and dark if it was never heard. The screen
    /// is blue while it reports a charging power and green while it reports none; the car and the cable are drawn only then.
    /// </summary>
    public sealed class LinkedChargerView
    {
        readonly AssetRig rig; readonly HarborMaterials mats;
        readonly MeshRenderer screen, link; readonly Material linkOn;
        public LinkedChargerView(AssetRig rig, HarborMaterials mats)
        {
            this.rig = rig; this.mats = mats;
            screen = rig["Screen"].GetComponent<MeshRenderer>(); link = rig["Link"].GetComponent<MeshRenderer>();
            linkOn = rig.Placement.Device.Kind == DeviceKind.CertCharger ? mats.SealOn : mats.LinkOn;
        }
        public void Apply(LinkedChargerBoard p)
        {
            var quiet = p.NoReading ? mats.LampUnknown : mats.LampSilent;
            screen.sharedMaterial = !p.Reporting ? quiet : p.Delivering ? mats.Flow : mats.StatusGreen;
            link.sharedMaterial = p.Reporting ? linkOn : quiet;
            rig["Car"].gameObject.SetActive(p.Delivering); rig["Cable"].gameObject.SetActive(p.Delivering);
        }
    }
}
