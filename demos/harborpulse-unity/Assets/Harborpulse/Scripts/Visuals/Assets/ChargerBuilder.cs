// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using DeviceChain.Harborpulse.Model;
using UnityEngine;

namespace DeviceChain.Harborpulse.Visuals
{
    public static class ChargerBuilder
    {
        public static AssetRig Build(Placement p, Transform parent, HarborMaterials m, Vector3 carAt, DetailManager detail = null)
        {
            var r = new AssetRig(p, parent);
            r.Part("Pedestal", new MeshKit().Box(Vector3.up * .95f, new Vector3(.7f, 1.9f, .5f))
                .Box(Vector3.up * 1.93f, new Vector3(.77f, .09f, .57f)), m.Charger, Vector3.zero);
            r.Box("Screen", new Vector3(0, 1.45f, 0.26f), new Vector3(0.48f, 0.36f, 0.035f), m.LampUnknown);
            var fittings = new MeshKit().Box(new Vector3(.42f, 1, .2f), new Vector3(.18f, .35f, .22f))
                .Box(new Vector3(0, 1.45f, .255f), new Vector3(.57f, .45f, .06f))
                .Box(new Vector3(0, .06f, 0), new Vector3(.85f, .12f, .7f))
                .Box(new Vector3(0, .12f, 1.25f), new Vector3(1.7f, .24f, .25f));
            // The stowed loop remains visible when no reported session supplies the car and active cable.
            var lastLoop = new Vector3(.43f, 1.05f, .17f);
            for (var i = 1; i <= 8; i++)
            {
                var a = i * Mathf.PI * 2 / 8;
                var next = new Vector3(.64f - .21f * Mathf.Cos(a), .66f + .39f * Mathf.Cos(a), .17f + .18f * Mathf.Sin(a));
                fittings.Beam(lastLoop, next, .045f); lastLoop = next;
            }
            r.Part("Holster", fittings, m.DarkSteel, Vector3.zero);
            r.Box("Connector", new Vector3(0.45f, 1.2f, 0.4f), new Vector3(0.12f, 0.25f, 0.22f), m.Flow);
            Bay(r, m, carAt, detail);
            r.Part("MaintenanceRing", new MeshKit().Ring(Vector3.zero, 3.4f, 0.06f), m.WorldCue, Vector3.up * 0.08f, false);
            r["CardAnchor"].localPosition = Vector3.up * 2.8f;
            r.MergeFixed("Holster", "BayMarkings");
            return r;
        }

        /// <summary>
        /// The bay in front of any charger: the car and the cable out to it (both shown only while the charger is seen delivering), and the
        /// bay's painted lines. The parts are named Car, Cable and BayMarkings.
        /// </summary>
        internal static void Bay(AssetRig r, HarborMaterials m, Vector3 carAt, DetailManager detail)
        {
            var car = r.Add("Car", carAt);
            new MeshKit().Box(new Vector3(0, 0.65f, 0), new Vector3((float)WorldPropPlan.CarBodyWidth, 0.65f, (float)WorldPropPlan.CarDepth)).Box(new Vector3(0, 1.16f, -0.2f), new Vector3(1.6f, 0.65f, 1.9f)).Spawn("UnbrandedHatchback", car, m.Render);
            new MeshKit().Box(new Vector3(0, 1.2f, 0.79f), new Vector3(1.5f, 0.45f, 0.04f)).Spawn("CarWindows", car, m.Glass);
            var wheels = new MeshKit();
            for (var side = -1; side <= 1; side += 2) for (var axle = -1; axle <= 1; axle += 2)
                wheels.Pipe(new Vector3(side * 0.78f, 0.4f, axle * 1.1f), Vector3.right * side, 0.34f, 0.22f, 8);
            wheels.Spawn("CarWheels", car, m.DarkSteel);
            // the car is only shown while a session is reported, so its near set follows the car's own visibility
            detail?.Register(NearVehicles.Car(CarSpec.Charging), car, Vector3.zero, Quaternion.identity, NearPalette.Vehicle(m.Render.GetColor("_BaseColor")), m.Surfaces, null, true,
                new[] { car.Find("UnbrandedHatchback").GetComponent<MeshRenderer>(), car.Find("CarWindows").GetComponent<MeshRenderer>(), car.Find("CarWheels").GetComponent<MeshRenderer>() });
            var cable = new MeshKit(); var last = new Vector3(0.45f, 1.1f, 0.4f);
            for (var i = 1; i <= 8; i++) { var t = i / 8f; var next = Vector3.Lerp(new Vector3(0.45f, 1.1f, 0.4f), carAt + new Vector3(.8f,.8f,-1), t) - Vector3.up * Mathf.Sin(t * Mathf.PI) * 0.75f; cable.Beam(last, next, 0.06f); last = next; }
            r.Part("Cable", cable, m.DarkSteel, Vector3.zero);
            var bay = new MeshKit();
            for (var side = -1; side <= 1; side += 2) bay.Box(carAt+new Vector3(side * 1.35f, 0.05f, -.9f), new Vector3(0.08f, 0.03f, 5.8f));
            bay.Box(carAt+new Vector3(0,.05f,2), new Vector3(2.7f, 0.03f, 0.08f));
            r.Part("BayMarkings", bay, m.GaugeBoard, Vector3.zero);
        }
    }
}
