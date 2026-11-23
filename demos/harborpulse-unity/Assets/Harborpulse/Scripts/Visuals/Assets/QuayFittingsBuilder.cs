// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Linq;
using DeviceChain.Harborpulse.Model;
using UnityEngine;

namespace DeviceChain.Harborpulse.Visuals
{
    /// <summary>
    /// Draws the plan in <see cref="QuayFittings"/>: under each pair of chargers that hold their own session a sett apron with a stone kerb,
    /// a feeder cabinet behind the pedestals, and chequer-plate trench covers from the cabinet to each pedestal and back to the quay road;
    /// at the pull lamps a sett landing by the wall, a ladder head on the coping and a lifebuoy. The pull lamps get no trench: they are on
    /// no cable. Flat surfaces go into one scenery mesh; everything upright is merged by material.
    /// </summary>
    internal static class QuayFittingsBuilder
    {
        const float Deck = (float)SitePlan.DeckHeightM;
        // vertex alpha is the scenery shader's surface code: .4 granite setts; 1 plain
        static readonly Color CourtSetts = new Color(.55f, .52f, .48f, .4f), LandingSetts = new Color(.46f, .42f, .37f, .4f);
        static readonly Color Kerb = new Color(.68f, .66f, .62f, 1), Plate = new Color(.33f, .34f, .35f, 1), PlateJoint = new Color(.2f, .21f, .22f, 1), PlateRail = new Color(.5f, .5f, .48f, 1);
        static readonly Color BayPaint = new Color(.64f, .61f, .48f, 1);

        static Vector3 V(Ground p, float y) => new Vector3((float)p.X, y, (float)p.Z);

        /// <summary>One quay's fittings under one node, or null when the plan lays nothing on that quay.</summary>
        public static Transform Build(Transform root, HarborMaterials m, QuayFittings fittings, string quay)
        {
            var courts = fittings.Courts.Where(c => c.Quay == quay).ToArray(); var landings = fittings.Landings.Where(l => l.Quay == quay).ToArray();
            if (courts.Length == 0 && landings.Length == 0) return null;
            var node = AssetRig.Node("Quay fittings " + quay, root, Vector3.zero);
            var flat = new SceneryMesh();
            foreach (var court in courts)
            {
                Apron(flat, court.Apron, CourtSetts, Deck + .012f);
                foreach (var duct in court.Ducts) Trench(flat, duct);
                // a hatched keep-clear panel between the two bays, in the bays' own faded paint
                var rot = Quaternion.Euler(0, (float)court.Frame.Yaw, 0);
                for (var i = -3; i <= 3; i++)
                    flat.Box(V(court.Frame.Local(0, 3.1 + i * .62), Deck + .03f), new Vector3(1.5f, .014f, .09f), BayPaint, rot * Quaternion.Euler(0, 38, 0));
                Cabinet(node, m, court);
            }
            foreach (var landing in landings)
            {
                // over the promenade's own strip, so a little higher than the chargers' apron
                Apron(flat, landing.Apron, LandingSetts, Deck + .024f);
                Ladder(node, m, landing);
            }
            if (flat.VertexCount > 0) flat.Spawn("Aprons and trench covers", node, m.Surfaces);
            AssetRig.MergeFixed(node, node.GetComponentsInChildren<MeshFilter>());
            return node;
        }

        static void Apron(SceneryMesh mesh, PropFootprint f, Color setts, float y)
        {
            var rot = Quaternion.Euler(0, (float)f.Yaw, 0);
            float w = (float)f.Width, d = (float)f.Depth; const float k = .28f;
            mesh.Box(V(f.At, y - .01f), new Vector3(w - 2 * k, .02f, d - 2 * k), setts, rot);
            // a flush kerb of lighter stone all round
            foreach (var side in new[] { -1, 1 })
            {
                mesh.Box(V(f.Local(side * (w - k) / 2, 0), y - .005f), new Vector3(k, .03f, d), Kerb, rot);
                mesh.Box(V(f.Local(0, side * (d - k) / 2), y - .005f), new Vector3(w - 2 * k, .03f, k), Kerb, rot);
            }
        }

        static void Trench(SceneryMesh mesh, DuctRun run)
        {
            var a = V(run.A, Deck + .03f); var b = V(run.B, Deck + .03f); var along = b - a; var length = along.magnitude;
            if (length < .05f) return;
            var rot = Quaternion.LookRotation(along / length, Vector3.up); var w = (float)run.Width;
            mesh.Box((a + b) / 2, new Vector3(w, .03f, length), Plate, rot);
            // the plates' joints, and the frame the plates sit in
            for (var s = .6f; s < length - .2f; s += 1.2f) mesh.Box(a + along * (s / length) + Vector3.up * .004f, new Vector3(w, .03f, .035f), PlateJoint, rot);
            foreach (var side in new[] { -1, 1 }) mesh.Box((a + b) / 2 + rot * Vector3.right * (side * (w / 2 + .03f)) + Vector3.up * .002f, new Vector3(.06f, .034f, length), PlateRail, rot);
        }

        static void Cabinet(Transform node, HarborMaterials m, ChargingCourt court)
        {
            var at = V(court.Cabinet, Deck); var yaw = (float)court.Frame.Yaw;
            float w = (float)QuayFittings.CabinetWidthM, d = (float)QuayFittings.CabinetDepthM;
            new MeshKit().Place(at, yaw).Box(new Vector3(0, .08f, 0), new Vector3(w + .16f, .16f, d + .16f)).Spawn("Cabinet plinth", node, m.Coping);
            new MeshKit().Place(at, yaw).Box(new Vector3(0, .88f, 0), new Vector3(w - .1f, 1.44f, d - .14f))
                .Box(new Vector3(0, 1.63f, 0), new Vector3(w, .07f, d - .04f)).Spawn("Cabinet", node, m.Steel);
            // door seam, hinges and louvres on the water side
            var dark = new MeshKit().Place(at, yaw).Box(new Vector3(0, .88f, d / 2 - .065f), new Vector3(.025f, 1.3f, .02f));
            for (var i = 0; i < 3; i++) dark.Box(new Vector3(-.36f, 1.28f - i * .09f, d / 2 - .065f), new Vector3(.34f, .035f, .02f));
            foreach (var y in new[] { .4f, 1.36f }) dark.Box(new Vector3(-w / 2 + .1f, y, d / 2 - .06f), new Vector3(.05f, .14f, .03f));
            // the band that says whose cabinet it is, in its chargers' own colour
            new MeshKit().Place(at, yaw).Box(new Vector3(0, 1.5f, 0), new Vector3(w - .08f, .12f, d - .12f)).Spawn("Cabinet band", node, court.Certificate ? m.ChargerCert : m.ChargerLink);
            if (court.Certificate)
                // a brass plate and its padlock hasp
                new MeshKit().Place(at, yaw).Box(new Vector3(.36f, .95f, d / 2 - .06f), new Vector3(.3f, .22f, .025f))
                    .Box(new Vector3(.06f, .9f, d / 2 - .055f), new Vector3(.08f, .12f, .04f)).Spawn("Cabinet plate", node, m.Brass);
            else
            {
                // a mast beside the cabinet with two panel aerials and a whip: the pair's own uplink
                var mast = new Vector3(w / 2 + .22f, 0, -.05f);
                new MeshKit().Place(at, yaw).Cylinder(mast, .07f, 6.2f, 8, .045f).Cylinder(mast, .16f, .18f, 8, .1f)
                    .Beam(mast + Vector3.up * 5.3f, mast + new Vector3(0, 5.3f, .3f), .04f).Beam(mast + Vector3.up * 5.75f, mast + new Vector3(.26f, 5.75f, -.15f), .04f)
                    .Spawn("Uplink mast", node, m.Steel);
                dark.Box(mast + new Vector3(0, 5.3f, .34f), new Vector3(.2f, .62f, .07f)).Box(mast + new Vector3(.3f, 5.75f, -.17f), new Vector3(.2f, .62f, .07f), 60)
                    .Cylinder(mast + Vector3.up * 6.2f, .018f, 1.1f, 5, .01f);
            }
            dark.Spawn("Cabinet details", node, m.DarkSteel);
        }

        static void Ladder(Transform node, HarborMaterials m, LampLanding landing)
        {
            // the landing's frame: +z towards the water, the wall's face at the ladder's own z
            var yaw = (float)landing.Frame.Yaw; var at = V(landing.Ladder, Deck);
            // the hoops rise to the railing's height and land on the coping, whose top is 0.55 m above the deck
            const float half = .26f, out_ = .22f, back = -1.25f, top = 1.55f, foot = -4.2f, coping = .55f;
            var rails = new MeshKit().Place(at, yaw);
            foreach (var side in new[] { -1, 1 })
            {
                var x = side * half;
                // up the wall's face, over the coping in a hoop, and down onto the landing
                rails.Beam(new Vector3(x, foot, out_), new Vector3(x, top - .2f, out_), .05f)
                    .Beam(new Vector3(x, top - .2f, out_), new Vector3(x, top, out_ - .2f), .05f)
                    .Beam(new Vector3(x, top, out_ - .2f), new Vector3(x, top, back + .2f), .05f)
                    .Beam(new Vector3(x, top, back + .2f), new Vector3(x, top - .2f, back), .05f)
                    .Beam(new Vector3(x, top - .2f, back), new Vector3(x, coping, back), .05f);
            }
            rails.Spawn("Ladder rails", node, m.Safety);
            var rungs = new MeshKit().Place(at, yaw);
            for (var y = foot + .2f; y < coping; y += .3f) rungs.Beam(new Vector3(-half, y, out_), new Vector3(half, y, out_), .035f);
            // a timber fender either side of the ladder, down the wall's face
            new MeshKit().Place(at, yaw).Box(new Vector3(-.75f, -1.9f, .12f), new Vector3(.24f, 4.4f, .24f)).Box(new Vector3(.75f, -1.9f, .12f), new Vector3(.24f, 4.4f, .24f)).Spawn("Ladder fenders", node, m.Piling);
            // a lifebuoy on a post beside the ladder, on the landing
            var post = Quaternion.Euler(0, -yaw, 0) * (V(landing.Lifebuoy, Deck) - at);
            rungs.Cylinder(post, .05f, 1.5f, 6).Box(post + new Vector3(0, 1.2f, .06f), new Vector3(.5f, .5f, .04f));
            rungs.Spawn("Ladder rungs", node, m.DarkSteel);
            new MeshKit().Place(at, yaw).Hoop(post + new Vector3(0, 1.2f, .12f), Vector3.forward, .3f, .1f).Spawn("Lifebuoy", node, m.BuoyRed);
        }
    }
}
