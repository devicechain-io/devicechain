// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Collections.Generic;
using DeviceChain.Harborpulse.Model;
using UnityEngine;

namespace DeviceChain.Harborpulse.Visuals
{
    /// <summary>
    /// The scene's cameras, as named shots derived from the site plan (the district is kilometres wide, so it has an overview and a close shot per
    /// quay rather than one framing that shows nothing). Keys 0-9 pick a shot; a mouse drag orbits about the shot's target and the wheel dollies.
    /// Input is read from IMGUI events, which work whichever input backend the player runs.
    /// </summary>
    public sealed class HarborCameraRig
    {
        public sealed class Shot
        {
            public string Name;
            public Vector3 Position;
            public Vector3 Target;
            public float Fov = 45;
        }

        readonly Camera cam;
        readonly List<Shot> shots = new List<Shot>();
        float yaw, pitch, dolly = 1;
        Vector3 pos, target;
        float fov;

        public HarborCameraRig(Camera camera, SitePlan plan)
        {
            cam = camera;
            var wall = (float)plan.WallZ;
            var cx = (float)(plan.MinX + plan.MaxX) / 2;
            shots.Add(new Shot { Name = "Overview", Position = new Vector3(cx + 300, 210, wall + 1250), Target = new Vector3(cx, 0, wall - 250), Fov = 55 });
            foreach (var q in plan.Quays)
                shots.Add(new Shot
                {
                    Name = q.Name, Position = new Vector3((float)q.CenterX + 140, 24, wall + 200), Target = new Vector3((float)q.CenterX, 3, wall - 12), Fov = 45,
                });

            var g = plan.Find("PAW-GAUGE-EAST-1");
            if (g != null)
                shots.Add(new Shot
                {
                    Name = "East Quay gauge", Position = new Vector3((float)g.At.X + 16, 5.5f, wall + 26), Target = new Vector3((float)g.At.X, 1.2f, wall), Fov = 40,
                });

            var lamp = plan.Find("PAW-LIGHT-NORTH-1");
            if (lamp != null)
                shots.Add(new Shot
                {
                    Name = "Promenade", Position = new Vector3((float)lamp.At.X - 60, 6, wall - 4), Target = new Vector3((float)lamp.At.X + 500, 3, (float)lamp.At.Z), Fov = 50,
                });
            AddClose(plan, "PAW-PUMP-NORTH", "North Quay pump", new Vector3(15, 6.5f, 15), new Vector3(9, -2.2f, 6), 46);
            AddClose(plan, "PAW-PUMP-BASIN", "Basin pump", new Vector3(15, 6.5f, 15), new Vector3(9, -2.2f, 6), 46);
            AddClose(plan, "PAW-CHARGER-NORTH-1", "North Quay chargers", new Vector3(7, 4, 12), new Vector3(3, 1, 2), 48);
            AddClose(plan, "PAW-GAUGE-NORTH-3", "North Quay gauge 3", new Vector3(6, 4, 10), new Vector3(0, 1, 0), 42);
            AddClose(plan, "PAW-LIGHT-EAST-3", "East lamp 3", new Vector3(13, 8, 17), new Vector3(0, 4, 0), 44);
            foreach (var p in plan.Placements) if (p.Device.Kind == DeviceKind.Truck) AddClose(plan, p.Device.ExternalId, "Truck", new Vector3(10, 6, 13), Vector3.up, 45);
            Select(0, true);
        }

        void AddClose(SitePlan plan, string id, string name, Vector3 offset, Vector3 targetOffset, float fov)
        {
            var p = plan.Find(id);
            if (p == null) throw new System.InvalidOperationException("shot device not in layout: " + id);
            var at = new Vector3((float)p.At.X, (float)SitePlan.DeckHeightM, (float)p.At.Z);
            shots.Add(new Shot { Name = name, Position = at + offset, Target = at + targetOffset, Fov = fov });
        }
        public void FollowTruck(TruckTrack truck)
        {
            var s = shots[shots.Count - 1];
            var at = new Vector3((float)truck.At.X, (float)SitePlan.DeckHeightM, (float)truck.At.Z);
            s.Position = at + new Vector3(10, 6, 13); s.Target = at + Vector3.up;
        }

        public IReadOnlyList<Shot> Shots => shots;
        public int Current { get; private set; }

        public void Select(int index, bool cut)
        {
            if (index < 0 || index >= shots.Count) return;
            Current = index;
            yaw = pitch = 0;
            dolly = 1;
            if (!cut) return;
            pos = shots[index].Position;
            target = shots[index].Target;
            fov = shots[index].Fov;
            Place();
        }

        /// <summary>Main thread, from OnGUI: shot keys, orbit and dolly.</summary>
        public void HandleEvent(Event e)
        {
            if (e == null) return;
            if (e.type == EventType.KeyDown && e.keyCode >= KeyCode.Alpha0 && e.keyCode <= KeyCode.Alpha9)
            {
                Select(e.keyCode - KeyCode.Alpha0, false);
                e.Use();
            }
            else if (e.type == EventType.KeyDown && (e.keyCode == KeyCode.F1 || e.keyCode == KeyCode.F2))
            {
                Select(e.keyCode == KeyCode.F1 ? 10 : 11, false); e.Use();
            }
            else if (e.type == EventType.MouseDrag && (e.button == 0 || e.button == 1))
            {
                yaw += e.delta.x * 0.25f;
                pitch = Mathf.Clamp(pitch - e.delta.y * 0.2f, -30, 40);
                e.Use();
            }
            else if (e.type == EventType.ScrollWheel)
            {
                dolly = Mathf.Clamp(dolly * (1 + e.delta.y * 0.05f), 0.15f, 3f);
                e.Use();
            }
        }

        public void Tick(float dt)
        {
            var s = shots[Current];
            var k = 1 - Mathf.Exp(-dt * 2.5f);
            target = Vector3.Lerp(target, s.Target, k);
            var offset = s.Position - s.Target;
            offset = Quaternion.AngleAxis(yaw, Vector3.up) * offset;
            var side = Vector3.Cross(Vector3.up, offset).normalized;
            offset = Quaternion.AngleAxis(pitch, side) * offset * dolly;
            pos = Vector3.Lerp(pos, s.Target + offset, k);
            fov = Mathf.Lerp(fov, s.Fov, k);
            Place();
        }

        void Place()
        {
            cam.transform.position = pos;
            cam.transform.rotation = Quaternion.LookRotation(target - pos, Vector3.up);
            cam.fieldOfView = fov;
        }
    }
}
