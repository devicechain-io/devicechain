// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Linq;
using DeviceChain.Harborpulse.Model;
using DeviceChain.Harborpulse.Model.Director;
using UnityEngine;

namespace DeviceChain.Harborpulse.Visuals
{
    /// <summary>Local +Z is water, +X runs along the wall; vertical metres are never scaled.</summary>
    public sealed class QuayFrame
    {
        public string Token { get; }
        public Vector3 Origin { get; }
        public Quaternion Rotation { get; }
        public float Length { get; }
        public float Depth { get; }
        public QuayFrame(PresentationMap map, string token)
        {
            Token = token; var p = map.Quays[token];
            var x0 = (float)p.Min(v => v.X); var x1 = (float)p.Max(v => v.X);
            var z0 = (float)p.Min(v => v.Z); var z1 = (float)p.Max(v => v.Z);
            var north = token == "hp-north-quay"; var east = token == "hp-east-quay";
            Origin = north ? new Vector3((x0 + x1) / 2, 0, z0) : new Vector3(east ? x0 : x1, 0, (z0 + z1) / 2);
            Rotation = Quaternion.Euler(0, north ? 180 : east ? 270 : 90, 0);
            Length = north ? x1 - x0 : z1 - z0; Depth = north ? z1 - z0 : x1 - x0;
        }
        public Vector3 World(Vector3 local) => Origin + Rotation * local;
        public Vector3 Local(Vector3 world) => Quaternion.Inverse(Rotation) * (world - Origin);
        public Transform Parent(Transform parent, string name)
        { var t = AssetRig.Node(name, parent, Origin); t.localRotation = Rotation; return t; }
    }
}
