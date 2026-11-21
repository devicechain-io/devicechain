// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Collections.Generic;
using UnityEngine;
using UnityEngine.Rendering;

namespace DeviceChain.Harborpulse.Visuals
{
    /// <summary>A single vertex-colour surface for rocks, land and foliage; flat normals, no texture or shadow pass.</summary>
    internal sealed class SceneryMesh
    {
        readonly List<Vector3> vertices = new List<Vector3>();
        readonly List<Vector3> normals = new List<Vector3>();
        readonly List<Color> colours = new List<Color>();
        readonly List<int> triangles = new List<int>();
        public int VertexCount => vertices.Count;

        public static Material Material()
        {
            // Resources guarantees inclusion in players even though all geometry is generated at runtime.
            var shader = Resources.Load<Shader>("HarborScenery");
            if (shader == null) throw new System.InvalidOperationException("HarborScenery shader missing from Resources");
            return new Material(shader) { name = "Harbor/coastal-scenery", enableInstancing = true };
        }

        public void Tri(Vector3 a, Vector3 b, Vector3 c, Color color)
        {
            color = color.linear;
            var n = Vector3.Cross(b - a, c - a).normalized;
            var start = vertices.Count;
            vertices.Add(a); vertices.Add(b); vertices.Add(c);
            for (var i = 0; i < 3; i++) { normals.Add(n); colours.Add(color); triangles.Add(start + i); }
        }

        public void Quad(Vector3 a, Vector3 b, Vector3 c, Vector3 d, Color color)
        {
            color = color.linear;
            var n = (Vector3.Cross(b - a, c - a) + Vector3.Cross(c - a, d - a)).normalized;
            var i = vertices.Count;
            vertices.Add(a); vertices.Add(b); vertices.Add(c); vertices.Add(d);
            for (var k = 0; k < 4; k++) { normals.Add(n); colours.Add(color); }
            triangles.Add(i); triangles.Add(i + 1); triangles.Add(i + 2);
            triangles.Add(i); triangles.Add(i + 2); triangles.Add(i + 3);
        }

        public void Box(Vector3 at, Vector3 size, Color color, Quaternion rotation)
        {
            Vector3 P(float x, float y, float z) => at + rotation * Vector3.Scale(new Vector3(x, y, z), size * .5f);
            Quad(P(-1,1,-1), P(-1,1,1), P(1,1,1), P(1,1,-1), color);
            Quad(P(-1,-1,-1), P(1,-1,-1), P(1,-1,1), P(-1,-1,1), color);
            Quad(P(-1,-1,-1), P(-1,1,-1), P(1,1,-1), P(1,-1,-1), color);
            Quad(P(1,-1,1), P(1,1,1), P(-1,1,1), P(-1,-1,1), color);
            Quad(P(-1,-1,1), P(-1,1,1), P(-1,1,-1), P(-1,-1,-1), color);
            Quad(P(1,-1,-1), P(1,1,-1), P(1,1,1), P(1,-1,1), color);
        }

        /// <summary>A quad whose visible side is the one facing <paramref name="outward"/>, whatever order its corners were given in.</summary>
        public void Facing(Vector3 a, Vector3 b, Vector3 c, Vector3 d, Vector3 outward, Color color)
        {
            var n = Vector3.Cross(b - a, c - a) + Vector3.Cross(c - a, d - a);
            if (Vector3.Dot(n, outward) < 0) Quad(d, c, b, a, color); else Quad(a, b, c, d, color);
        }

        /// <summary>A triangle whose visible side is the one facing <paramref name="outward"/>.</summary>
        public void Facing(Vector3 a, Vector3 b, Vector3 c, Vector3 outward, Color color)
        {
            if (Vector3.Dot(Vector3.Cross(b - a, c - a), outward) < 0) Tri(a, c, b, color); else Tri(a, b, c, color);
        }

        /// <summary>A square-section strut from <paramref name="a"/> to <paramref name="b"/> (the same cross-section as MeshKit.Beam).</summary>
        public void Beam(Vector3 a, Vector3 b, float width, float depth, Color color)
        {
            var axis = b - a;
            if (axis.sqrMagnitude < 1e-6f) return;
            axis.Normalize();
            var u = Vector3.Cross(axis, Mathf.Abs(axis.y) < .95f ? Vector3.up : Vector3.forward).normalized;
            var v = Vector3.Cross(axis, u);
            Vector3 U = u * width * .5f, V = v * depth * .5f;
            Facing(a - U - V, a - U + V, a + U + V, a + U - V, -axis, color);
            Facing(b - U - V, b - U + V, b + U + V, b + U - V, axis, color);
            Facing(a + U - V, a + U + V, b + U + V, b + U - V, u, color);
            Facing(a - U - V, a - U + V, b - U + V, b - U - V, -u, color);
            Facing(a - U + V, a + U + V, b + U + V, b - U + V, v, color);
            Facing(a - U - V, a + U - V, b + U - V, b - U - V, -v, color);
        }

        /// <summary>A capped cylinder (or cone frustum) from base centre <paramref name="a"/> to top centre <paramref name="b"/>, in any direction.</summary>
        public void Cylinder(Vector3 a, Vector3 b, float radius, float topRadius, int segments, Color color)
        {
            var axis = b - a;
            if (axis.sqrMagnitude < 1e-8f) return;
            axis.Normalize();
            var rotation = Quaternion.FromToRotation(Vector3.up, axis);
            for (var i = 0; i < segments; i++)
            {
                float t0 = i * Mathf.PI * 2 / segments, t1 = (i + 1) * Mathf.PI * 2 / segments;
                var d0 = rotation * new Vector3(Mathf.Cos(t0), 0, Mathf.Sin(t0));
                var d1 = rotation * new Vector3(Mathf.Cos(t1), 0, Mathf.Sin(t1));
                Facing(a + d0 * radius, b + d0 * topRadius, b + d1 * topRadius, a + d1 * radius, d0 + d1, color);
                if (topRadius > 0) Facing(b, b + d1 * topRadius, b + d0 * topRadius, axis, color);
                Facing(a, a + d0 * radius, a + d1 * radius, -axis, color);
            }
        }

        /// <summary>A gabled prism over a footprint centred on <paramref name="center"/> (its eaves), ridge along local X (see MeshKit.Gable).</summary>
        public void Gable(Vector3 center, float width, float depth, float rise, float yawDeg, Color color)
        {
            var r = Quaternion.Euler(0, yawDeg, 0); float hw = width / 2, hd = depth / 2;
            Vector3 P(float x, float y, float z) => center + r * new Vector3(x, y, z);
            Vector3 a = P(-hw, 0, -hd), b = P(hw, 0, -hd), c = P(hw, 0, hd), d = P(-hw, 0, hd), e = P(-hw, rise, 0), f = P(hw, rise, 0);
            Facing(a, e, f, b, r * new Vector3(0, hd, -rise), color);
            Facing(c, f, e, d, r * new Vector3(0, hd, rise), color);
            Facing(a, d, e, r * Vector3.left, color);
            Facing(b, f, c, r * Vector3.right, color);
        }

        public void Branch(Vector3 a, Vector3 b, float radius, float topRadius, Color color, int sides = 5)
        {
            var rotation = Quaternion.FromToRotation(Vector3.up, (b - a).normalized);
            for (var i = 0; i < sides; i++)
            {
                Vector3 Ring(int j) { var t = j * Mathf.PI * 2 / sides; return rotation * new Vector3(Mathf.Cos(t), 0, Mathf.Sin(t)); }
                var u = Ring(i); var v = Ring(i + 1);
                if (topRadius == 0) Tri(a + u * radius, b, a + v * radius, color);
                else Quad(a + u * radius, b + u * topRadius, b + v * topRadius, a + v * radius, color);
                // Crowns and trunks have closed tops; the buried bottom is deliberately omitted.
                if (topRadius > 0) Tri(b, b + v * topRadius, b + u * topRadius, color);
            }
        }

        public void Crown(Vector3 center, Vector3 radii, Color color, int detail, float yaw = 0)
        {
            var rot = Quaternion.Euler(0, yaw, 0);
            Vector3 P(Vector3 v) => center + rot * Vector3.Scale(v, radii);
            Vector3[] points = { Vector3.up, Vector3.forward, Vector3.right, Vector3.back, Vector3.left, Vector3.down };
            int[,] faces = { {0,1,2}, {0,2,3}, {0,3,4}, {0,4,1}, {5,2,1}, {5,3,2}, {5,4,3}, {5,1,4} };
            for (var f = 0; f < 8; f++)
            {
                var a = points[faces[f,0]]; var b = points[faces[f,1]]; var c = points[faces[f,2]];
                if (detail == 0) { Tri(P(a), P(b), P(c), color); continue; }
                var ab = (a + b).normalized; var bc = (b + c).normalized; var ca = (c + a).normalized;
                Tri(P(a), P(ab), P(ca), color); Tri(P(ab), P(b), P(bc), color);
                Tri(P(ca), P(bc), P(c), color); Tri(P(ab), P(bc), P(ca), color);
            }
        }

        public GameObject Spawn(string name, Transform parent, Material material, int layer = 0)
        {
            var mesh = new Mesh { name = name, indexFormat = vertices.Count > 65000 ? IndexFormat.UInt32 : IndexFormat.UInt16 };
            mesh.SetVertices(vertices); mesh.SetNormals(normals); mesh.SetColors(colours); mesh.SetTriangles(triangles, 0); mesh.RecalculateBounds();
            var go = new GameObject(name) { layer = layer };
            go.transform.SetParent(parent, false);
            go.AddComponent<MeshFilter>().sharedMesh = mesh;
            var renderer = go.AddComponent<MeshRenderer>();
            renderer.sharedMaterial = material; renderer.shadowCastingMode = ShadowCastingMode.Off;
            return go;
        }
    }
}
