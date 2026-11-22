// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using DeviceChain.Harborpulse.Model;
using DeviceChain.Harborpulse.Model.Director;
using UnityEngine;

namespace DeviceChain.Harborpulse.Visuals
{
    /// <summary>Camera ownership and interpolation only; no evidence or scenario triggers.</summary>
    public sealed class DirectorCamera
    {
        readonly Camera camera;
        readonly PresentationMap map;
        readonly HashSet<KeyCode> held = new HashSet<KeyCode>();
        Vector3 freePosition, freeLook, moveStart;
        Quaternion freeRotation, moveRotation;
        float freeFov, moveFov;
        double shotAt = double.NaN;
        bool navigating;
        double rejoinAt = double.NegativeInfinity;
        public void Rejoin(double now) { Take(); rejoinAt = now; }
        public bool Portrait { get; set; }
        public DirectorCamera(Camera camera, PresentationMap map, bool portrait)
        { this.camera = camera; this.map = map; Portrait = portrait; ForcePose(portrait ? "pose:home:portrait" : "pose:home"); }
        public static Vector3 V(Point3 p) => new Vector3((float)p.X, (float)p.Y, (float)p.Z);
        public void ForcePose(string name)
        {
            var p = map.Pose(name); camera.transform.position = V(p.Camera);
            camera.transform.rotation = Quaternion.LookRotation(V(p.Look) - V(p.Camera)); camera.fieldOfView = (float)p.VerticalFov;
            Take(); shotAt = double.NaN;
        }
        void Take()
        {
            freePosition = camera.transform.position; freeRotation = camera.transform.rotation; freeFov = camera.fieldOfView;
            freeLook = freePosition + freeRotation * Vector3.forward * 50;
        }
        static string Nav(KeyCode key)
        {
            switch (key)
            {
                case KeyCode.W: return "W"; case KeyCode.A: return "A"; case KeyCode.S: return "S"; case KeyCode.Q: return "Q";
                case KeyCode.UpArrow: case KeyCode.PageUp: return "ArrowUp"; case KeyCode.DownArrow: return "ArrowDown";
                case KeyCode.LeftArrow: return "ArrowLeft"; case KeyCode.RightArrow: return "ArrowRight"; default: return null;
            }
        }
        public void HandleEvent(Event e, DirectorHost host, double now)
        {
            if (e == null) return;
            if (e.type == EventType.KeyUp) held.Remove(e.keyCode);
            var drag = e.type == EventType.MouseDrag && (e.button == 0 || e.button == 1);
            var wheel = e.type == EventType.ScrollWheel;
            var key = e.type == EventType.KeyDown ? Nav(e.keyCode) : null;
            if (key == null && !drag && !wheel) return;
            if (!navigating) Take(); navigating = true;
            host.FreeRoam.Input(now, key, drag, wheel);
            if (key != null) held.Add(e.keyCode);
            if (drag)
            {
                var orbit = Quaternion.AngleAxis(e.delta.x * .25f, Vector3.up) * Quaternion.AngleAxis(-e.delta.y * .2f, freeRotation * Vector3.right);
                freePosition = freeLook + orbit * (freePosition - freeLook);
                freeRotation = Quaternion.LookRotation(freeLook - freePosition);
            }
            if (wheel) freePosition += freeRotation * Vector3.forward * (-e.delta.y * 3);
            camera.transform.SetPositionAndRotation(freePosition, freeRotation); camera.fieldOfView = freeFov;
            e.Use();
        }
        public void Tick(DirectorView view, DirectorHost host, double now, float dt, IReadOnlyList<TimedFix> fixes)
        {
            if (held.Count > 0)
            {
                foreach (var key in held)
                {
                    host.FreeRoam.Input(now, Nav(key));
                    var direction = key == KeyCode.W || key == KeyCode.UpArrow ? freeRotation * Vector3.forward :
                        key == KeyCode.S || key == KeyCode.DownArrow ? freeRotation * Vector3.back :
                        key == KeyCode.A || key == KeyCode.LeftArrow ? freeRotation * Vector3.left :
                        key == KeyCode.RightArrow ? freeRotation * Vector3.right : key == KeyCode.PageUp ? Vector3.up : Vector3.down;
                    freePosition += direction * Mathf.Max(0, dt) * 28; freeLook += direction * Mathf.Max(0, dt) * 28;
                }
            }
            var weight = host.Enabled && view.Mode != DirectorMode.LayoutMismatch ? host.FreeRoam.DirectorWeight(now) : 0;
            var rejoin = Mathf.Clamp01((float)((now - rejoinAt) / 3));
            weight *= rejoin * rejoin * (3 - 2 * rejoin);
            if (shotAt != view.ShotAtS)
            {
                shotAt = view.ShotAtS; moveStart = camera.transform.position; moveRotation = camera.transform.rotation; moveFov = camera.fieldOfView;
            }
            var pose = Portrait && view.Shot == null && view.AmbientPose == "pose:home" ? map.Pose("pose:home:portrait") : Portrait ? map.Tall(view.To) : view.To;
            var pos = V(pose.Camera); var look = V(pose.Look);
            if (view.FollowDevice == "hp-truck-1")
            {
                var at = map.TruckAt(fixes, now).At;
                var shift = new Vector3((float)at.X - look.x, 0, (float)at.Z - look.z); pos += shift; look += shift;
            }
            else if (view.FollowAnchor.HasValue)
            {
                var at = view.FollowAnchor.Value;
                var shift = new Vector3((float)at.X - look.x, 0, (float)at.Z - look.z); pos += shift; look += shift;
            }
            var t = view.MoveS <= 0 ? 1 : Mathf.Clamp01((float)((now - shotAt) / view.MoveS)); t = t * t * (3 - 2 * t);
            var directorPosition = Vector3.Lerp(moveStart, pos, t);
            var directorRotation = Quaternion.Slerp(moveRotation, Quaternion.LookRotation(look - pos), t);
            camera.transform.SetPositionAndRotation(Vector3.Lerp(freePosition, directorPosition, (float)weight), Quaternion.Slerp(freeRotation, directorRotation, (float)weight));
            camera.fieldOfView = Mathf.Lerp(freeFov, Mathf.Lerp(moveFov, (float)pose.VerticalFov, t), (float)weight);
            if (weight >= 1) navigating = false;
        }
        public void AdoptPreset() { Take(); navigating = true; }
    }
}
