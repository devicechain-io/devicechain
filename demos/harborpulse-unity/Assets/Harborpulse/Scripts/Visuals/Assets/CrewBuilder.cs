// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using UnityEngine;

namespace DeviceChain.Harborpulse.Visuals
{
    /// <summary>
    /// The WORLD crew at a job: a utility van, a line of cones and a work barrier, and at a street light a bucket lift with a technician
    /// at the lantern. It is a cue the scenario declares, never platform evidence, so it keeps the cool scene palette: white and steel,
    /// the fleet's blue stripe, and the pale blue-white of every other WORLD cue for the cones, the barrier and the beacon bar. Amber,
    /// coral and green stay the platform's. Everything is merged by material when it is built.
    /// </summary>
    public static class CrewBuilder
    {
        /// <summary>Where the bucket's floor hangs in the crew's own frame: beside the lantern of the lamp the crew was built for (which stands at (-4, 0, 3)).</summary>
        static readonly Vector3 Bucket = new Vector3(-3, 6.9f, 2);

        public static Transform Build(Transform parent, HarborMaterials m, bool lamp, Vector3 at)
        {
            var t = AssetRig.Node(lamp ? "LampCrew" : "ReplacementCrew", parent, at);
            Van(t, m);
            Cones(t, m);
            if (lamp) Lift(t, m); else Kit(t, m);
            AssetRig.MergeFixed(t, t.GetComponentsInChildren<MeshFilter>());
            t.gameObject.SetActive(false); return t;
        }

        /// <summary>The van: a cab with a short bonnet in front of an open service body between two side cabinets. It faces +Z.</summary>
        static void Van(Transform t, HarborMaterials m)
        {
            var paint = new MeshKit()
                .Box(new Vector3(0, 1.22f, 1.72f), new Vector3(2.1f, 1.0f, 1.9f))       // cab, below the glass
                .Box(new Vector3(0, 2.36f, 1.55f), new Vector3(1.98f, .1f, 1.62f))      // roof
                .Box(new Vector3(0, 1.06f, 2.92f), new Vector3(2.02f, .68f, .52f));     // bonnet
            // the glasshouse: four corner pillars and a rear wall, so the glass sits in a frame
            for (var s = -1; s <= 1; s += 2)
            {
                paint.Beam(new Vector3(s * .95f, 1.7f, 2.4f), new Vector3(s * .93f, 2.32f, 2.16f), .1f)
                    .Beam(new Vector3(s * .95f, 1.7f, .82f), new Vector3(s * .95f, 2.32f, .82f), .1f)
                    .Box(new Vector3(s * .82f, 1.42f, -1.0f), new Vector3(.46f, .98f, 3.5f));   // side cabinet
            }
            paint.Box(new Vector3(0, 2.0f, .8f), new Vector3(1.9f, .62f, .08f));
            paint.Spawn("VanBody", t, m.Render);

            var glass = new MeshKit().Box(new Vector3(0, 2.01f, 2.3f), new Vector3(1.78f, .56f, .04f));
            for (var s = -1; s <= 1; s += 2) glass.Box(new Vector3(s * .97f, 2.01f, 1.6f), new Vector3(.04f, .54f, 1.42f));
            glass.Spawn("VanWindows", t, m.Glass);

            var dark = new MeshKit()
                .Box(new Vector3(0, .62f, 0), new Vector3(1.86f, .22f, 5.5f))            // chassis
                .Box(new Vector3(0, .62f, 3.24f), new Vector3(2.16f, .26f, .2f))        // front bumper
                .Box(new Vector3(0, .62f, -2.82f), new Vector3(2.16f, .22f, .16f))      // rear step bumper
                .Box(new Vector3(0, 1.1f, 3.19f), new Vector3(1.2f, .36f, .04f))        // grille
                .Box(new Vector3(0, 1.72f, 2.5f), new Vector3(1.9f, .05f, .3f));        // scuttle under the screen
            for (var s = -1; s <= 1; s += 2)
            {
                for (var a = -1; a <= 1; a += 2)
                {
                    var wz = a > 0 ? 1.95f : -1.55f;
                    dark.Pipe(new Vector3(s * .82f, .42f, wz), Vector3.right * s, .42f, .26f, 12)
                        .Box(new Vector3(s * 1.0f, .93f, wz), new Vector3(.3f, .08f, 1.06f));    // arch lip
                }
                // compartment seams and the mirror
                for (var k = 0; k < 4; k++) dark.Box(new Vector3(s * 1.056f, 1.42f, -2.7f + k * 1.13f), new Vector3(.012f, .9f, .03f));
                dark.Box(new Vector3(s * 1.056f, 1.9f, -1.0f), new Vector3(.012f, .03f, 3.42f))
                    .Beam(new Vector3(s * 1.0f, 1.95f, 2.3f), new Vector3(s * 1.28f, 1.95f, 2.2f), .045f)
                    .Box(new Vector3(s * 1.3f, 1.95f, 2.2f), new Vector3(.08f, .26f, .16f));
            }
            dark.Spawn("VanChassisAndTyres", t, m.DarkSteel);

            var steel = new MeshKit().Box(new Vector3(0, .88f, -1.0f), new Vector3(1.2f, .1f, 3.5f));   // the bed between the cabinets
            for (var s = -1; s <= 1; s += 2)
            {
                for (var a = -1; a <= 1; a += 2) steel.Pipe(new Vector3(s * 1.075f, .42f, a > 0 ? 1.95f : -1.55f), Vector3.right * s, .23f, .02f, 10);
                for (var k = 0; k < 3; k++) steel.Box(new Vector3(s * 1.062f, 1.5f, -2.14f + k * 1.13f), new Vector3(.02f, .04f, .2f));   // handles
            }
            steel.Spawn("VanBedRimsHandles", t, m.Steel);

            var light = new MeshKit();
            for (var s = -1; s <= 1; s += 2)
                light.Box(new Vector3(s * .78f, 1.12f, 3.19f), new Vector3(.3f, .2f, .04f)).Box(new Vector3(s * 1.06f, 1.22f, -1.0f), new Vector3(.014f, .1f, 3.5f));
            light.Spawn("VanLampsAndPaleStripe", t, m.GaugeBoard);

            var stripe = new MeshKit();
            for (var s = -1; s <= 1; s += 2)
                stripe.Box(new Vector3(s * 1.06f, 1.06f, -1.0f), new Vector3(.014f, .18f, 3.5f)).Box(new Vector3(s * 1.056f, 1.06f, 1.72f), new Vector3(.014f, .18f, 1.9f));
            stripe.Spawn("VanFleetStripe", t, m.Charger);

            // the beacon bar is a WORLD cue's cool white, never the platform's amber
            new MeshKit().Box(new Vector3(0, 2.47f, 1.55f), new Vector3(1.1f, .12f, .26f)).Spawn("VanBeaconBar", t, m.WorldCue);
        }

        /// <summary>Six cones round the front of the job and a two-board barrier behind it.</summary>
        static void Cones(Transform t, HarborMaterials m)
        {
            var cones = new MeshKit(); var bands = new MeshKit();
            var at = new[] { new Vector2(-2.7f, 3.3f), new Vector2(-1.3f, 3.5f), new Vector2(.1f, 3.9f), new Vector2(1.5f, 3.9f), new Vector2(2.6f, 3.2f), new Vector2(2.9f, 1.4f) };
            foreach (var p in at)
            {
                var foot = new Vector3(p.x, 0, p.y);
                cones.Box(foot + Vector3.up * .035f, new Vector3(.5f, .07f, .5f)).Cylinder(foot + Vector3.up * .07f, .19f, .66f, 8, .04f);
                // the reflective collar, a hair wider than the cone where it sits
                bands.Cylinder(foot + Vector3.up * .32f, .135f, .15f, 8, .1f);
            }
            cones.Spawn("TrafficCones", t, m.WorldCue);
            bands.Spawn("ConeCollars", t, m.GaugeBoard);
            var barrier = new MeshKit().Box(new Vector3(-3, 1.02f, -1.2f), new Vector3(.1f, .22f, 3)).Box(new Vector3(-3, .6f, -1.2f), new Vector3(.1f, .16f, 3));
            for (var a = -1; a <= 1; a += 2)
                barrier.Beam(new Vector3(-3.28f, 0, -1.2f + a * 1.35f), new Vector3(-3, 1.2f, -1.2f + a * 1.35f), .07f)
                    .Beam(new Vector3(-2.72f, 0, -1.2f + a * 1.35f), new Vector3(-3, 1.2f, -1.2f + a * 1.35f), .07f);
            barrier.Spawn("WorkBarrier", t, m.WorldCue);
        }

        /// <summary>The bucket lift: a turret on the bed, two boom sections with a knuckle and a ram, outriggers down, and a technician in the bucket.</summary>
        static void Lift(Transform t, HarborMaterials m)
        {
            var turret = new Vector3(0, .93f, -1.7f); var knuckle = new Vector3(-1.25f, 5.1f, -.5f);
            var boom = new MeshKit()
                .Beam(turret + Vector3.up * .7f, knuckle, .3f, .26f)
                .Beam(knuckle, Bucket + new Vector3(.2f, -.05f, -.25f), .22f, .2f)
                .Box(Bucket + Vector3.up * .04f, new Vector3(1.15f, .08f, 1.15f));
            // the bucket's four walls, open at the top
            boom.Box(Bucket + new Vector3(.55f, .58f, 0), new Vector3(.06f, 1.08f, 1.15f)).Box(Bucket + new Vector3(-.55f, .58f, 0), new Vector3(.06f, 1.08f, 1.15f))
                .Box(Bucket + new Vector3(0, .58f, .55f), new Vector3(1.15f, 1.08f, .06f)).Box(Bucket + new Vector3(0, .58f, -.55f), new Vector3(1.15f, 1.08f, .06f));
            boom.Spawn("BucketLift", t, m.Render);

            var steel = new MeshKit().Cylinder(turret, .44f, .72f, 10).Ball(knuckle, .24f)
                .Rod(turret + new Vector3(-.2f, .5f, .15f), Vector3.Lerp(turret + Vector3.up * .7f, knuckle, .45f), .07f)
                .Rod(Vector3.Lerp(turret + Vector3.up * .7f, knuckle, .72f), Vector3.Lerp(knuckle, Bucket, .3f) + Vector3.down * .25f, .06f);
            // the bucket's rim
            steel.Box(Bucket + new Vector3(0, 1.14f, .55f), new Vector3(1.24f, .06f, .1f)).Box(Bucket + new Vector3(0, 1.14f, -.55f), new Vector3(1.24f, .06f, .1f))
                .Box(Bucket + new Vector3(.55f, 1.14f, 0), new Vector3(.1f, .06f, 1.24f)).Box(Bucket + new Vector3(-.55f, 1.14f, 0), new Vector3(.1f, .06f, 1.24f));
            steel.Spawn("LiftTurretRamsRim", t, m.Steel);

            var legs = new MeshKit();
            for (var s = -1; s <= 1; s += 2)
                legs.Beam(new Vector3(s * 1.0f, .8f, -2.45f), new Vector3(s * 1.5f, .1f, -2.45f), .13f).Box(new Vector3(s * 1.5f, .04f, -2.45f), new Vector3(.5f, .08f, .5f));
            legs.Spawn("Outriggers", t, m.DarkSteel);

            // the technician: coveralls in the fleet's blue, a white helmet, one arm up at the lantern
            var chest = Bucket + new Vector3(-.12f, 1.42f, .12f);
            new MeshKit().Box(chest, new Vector3(.44f, .6f, .26f), 35).Box(chest + new Vector3(0, -.55f, 0), new Vector3(.4f, .5f, .24f), 35)
                .Beam(chest + new Vector3(-.2f, .22f, .12f), chest + new Vector3(-.52f, .62f, .5f), .11f)
                .Beam(chest + new Vector3(.22f, .2f, -.06f), chest + new Vector3(.3f, -.2f, .16f), .11f)
                .Spawn("TechnicianCoveralls", t, m.Charger);
            new MeshKit().Ball(chest + new Vector3(0, .5f, 0), .15f).Cylinder(chest + new Vector3(0, .52f, 0), .19f, .03f, 10)
                .Spawn("TechnicianHelmet", t, m.GaugeBoard);
        }

        /// <summary>A replacement crew has no lift: a ladder on the van's roof and a tool case on the ground.</summary>
        static void Kit(Transform t, HarborMaterials m)
        {
            var kit = new MeshKit();
            for (var s = -1; s <= 1; s += 2) kit.Beam(new Vector3(s * .28f, 2.5f, 2.2f), new Vector3(s * .28f, 2.5f, -2.6f), .06f);
            for (var z = -2.4f; z < 2.2f; z += .4f) kit.Beam(new Vector3(-.28f, 2.5f, z), new Vector3(.28f, 2.5f, z), .045f);
            kit.Box(new Vector3(-2.2f, .2f, 1.2f), new Vector3(.7f, .4f, .4f));
            kit.Spawn("LadderAndToolCase", t, m.Steel);
        }
    }
}
