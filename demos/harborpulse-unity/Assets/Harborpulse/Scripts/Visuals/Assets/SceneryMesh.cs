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
