// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Collections.Generic;
using UnityEngine;
using UnityEngine.Rendering;

namespace DeviceChain.Harborpulse.Visuals
{
    /// <summary>
    /// The scene's art kit: flat-shaded low-poly primitives (boxes, cylinders, gabled roofs) appended into one mesh, built in code with fixed
    /// dimensions so the same district comes out every time. No model or texture file is involved; this is the whole asset pipeline.
    /// </summary>
    public sealed class MeshKit
    {
        readonly List<Vector3> vertices = new List<Vector3>();
        readonly List<Vector3> normals = new List<Vector3>();
        readonly List<int> triangles = new List<int>();
        Matrix4x4 placement = Matrix4x4.identity;
        bool placed;

        public int VertexCount => vertices.Count;

        /// <summary>
        /// Places what is appended next: every later primitive is built in a local frame at <paramref name="origin"/>, turned by
        /// <paramref name="yawDeg"/> about the vertical, until the next call. One kit can then batch many placed objects into one mesh.
        /// </summary>
        public MeshKit Place(Vector3 origin, float yawDeg)
        {
            placement = Matrix4x4.TRS(origin, Quaternion.Euler(0, yawDeg, 0), Vector3.one);
            placed = true;
            return this;
        }

        Vector3 X(Vector3 p) => placed ? placement.MultiplyPoint3x4(p) : p;

        /// <summary>A quad given in order round its face (the face is the side from which the order runs clockwise).</summary>
        public void Quad(Vector3 a, Vector3 b, Vector3 c, Vector3 d)
        {
            a = X(a); b = X(b); c = X(c); d = X(d);
            // both triangles' normals, so a quad with one collapsed edge (a cone's side) still has a normal: a zero normal shades as NaN.
            // A quad collapsed to a line or a point still has none; nothing here builds one.
            var n = (Vector3.Cross(b - a, c - a) + Vector3.Cross(c - a, d - a)).normalized;
            var i = vertices.Count;
            vertices.Add(a);
            vertices.Add(b);
            vertices.Add(c);
            vertices.Add(d);
            for (var k = 0; k < 4; k++) normals.Add(n);
            triangles.Add(i);
            triangles.Add(i + 1);
            triangles.Add(i + 2);
            triangles.Add(i);
            triangles.Add(i + 2);
            triangles.Add(i + 3);
        }

        void Tri(Vector3 a, Vector3 b, Vector3 c)
        {
            a = X(a); b = X(b); c = X(c);
            var n = Vector3.Cross(b - a, c - a).normalized;
            var i = vertices.Count;
            vertices.Add(a);
            vertices.Add(b);
            vertices.Add(c);
            for (var k = 0; k < 3; k++) normals.Add(n);
            triangles.Add(i);
            triangles.Add(i + 1);
            triangles.Add(i + 2);
        }

        /// <summary>An axis-aligned box (then turned by <paramref name="yawDeg"/> about its centre) given its centre and full size.</summary>
        public MeshKit Box(Vector3 center, Vector3 size, float yawDeg = 0)
        {
            var r = Quaternion.Euler(0, yawDeg, 0);
            var h = size * 0.5f;
            Vector3 P(float x, float y, float z) => center + r * new Vector3(x * h.x, y * h.y, z * h.z);
            Vector3 p000 = P(-1, -1, -1), p100 = P(1, -1, -1), p010 = P(-1, 1, -1), p110 = P(1, 1, -1);
            Vector3 p001 = P(-1, -1, 1), p101 = P(1, -1, 1), p011 = P(-1, 1, 1), p111 = P(1, 1, 1);
            Quad(p010, p011, p111, p110); // top
            Quad(p000, p100, p101, p001); // bottom
            Quad(p000, p010, p110, p100); // -z
            Quad(p101, p111, p011, p001); // +z
            Quad(p001, p011, p010, p000); // -x
            Quad(p100, p110, p111, p101); // +x
            return this;
        }

        /// <summary>A box from its footprint's minimum corner and its top: handy for slabs and walls.</summary>
        public MeshKit Slab(float x0, float x1, float z0, float z1, float y0, float y1) =>
            Box(new Vector3((x0 + x1) / 2, (y0 + y1) / 2, (z0 + z1) / 2), new Vector3(Mathf.Abs(x1 - x0), Mathf.Abs(y1 - y0), Mathf.Abs(z1 - z0)));

        /// <summary>An upright cylinder standing on <paramref name="baseCenter"/>.</summary>
        public MeshKit Cylinder(Vector3 baseCenter, float radius, float height, int segments = 10, float topRadius = -1)
        {
            if (topRadius < 0) topRadius = radius;
            var top = baseCenter + Vector3.up * height;
            for (var s = 0; s < segments; s++)
            {
                float a0 = s * Mathf.PI * 2 / segments, a1 = (s + 1) * Mathf.PI * 2 / segments;
                var d0 = new Vector3(Mathf.Cos(a0), 0, Mathf.Sin(a0));
                var d1 = new Vector3(Mathf.Cos(a1), 0, Mathf.Sin(a1));
                Quad(baseCenter + d0 * radius, top + d0 * topRadius, top + d1 * topRadius, baseCenter + d1 * radius);
                if (topRadius > 0) Tri(top, top + d1 * topRadius, top + d0 * topRadius);
                Tri(baseCenter, baseCenter + d0 * radius, baseCenter + d1 * radius);
            }

            return this;
        }

        /// <summary>A horizontal cylinder along <paramref name="axis"/> (unit) from <paramref name="start"/>.</summary>
        public MeshKit Pipe(Vector3 start, Vector3 axis, float radius, float length, int segments = 10)
        {
            var rot = Quaternion.FromToRotation(Vector3.up, axis.normalized);
            var end = start + axis.normalized * length;
            for (var s = 0; s < segments; s++)
            {
                float a0 = s * Mathf.PI * 2 / segments, a1 = (s + 1) * Mathf.PI * 2 / segments;
                var d0 = rot * new Vector3(Mathf.Cos(a0), 0, Mathf.Sin(a0)) * radius;
                var d1 = rot * new Vector3(Mathf.Cos(a1), 0, Mathf.Sin(a1)) * radius;
                Quad(start + d0, end + d0, end + d1, start + d1);
                Tri(end, end + d1, end + d0);
            }

            return this;
        }

        /// <summary>A low-poly ball (an octahedron split once and pushed out to the sphere).</summary>
        public MeshKit Ball(Vector3 center, float radius)
        {
            Vector3[] o = { Vector3.up, Vector3.forward, Vector3.right, Vector3.back, Vector3.left, Vector3.down };
            int[,] faces = { { 0, 1, 2 }, { 0, 2, 3 }, { 0, 3, 4 }, { 0, 4, 1 }, { 5, 2, 1 }, { 5, 3, 2 }, { 5, 4, 3 }, { 5, 1, 4 } };
            for (var f = 0; f < 8; f++)
            {
                Vector3 a = o[faces[f, 0]], b = o[faces[f, 1]], c = o[faces[f, 2]];
                Vector3 ab = (a + b).normalized, bc = (b + c).normalized, ca = (c + a).normalized;
                Vector3 P(Vector3 v) => center + v * radius;
                Tri(P(a), P(ab), P(ca));
                Tri(P(ab), P(b), P(bc));
                Tri(P(ca), P(bc), P(c));
                Tri(P(ab), P(bc), P(ca));
            }

            return this;
        }

        /// <summary>
        /// A faceted ellipsoid: an octahedron (<paramref name="detail"/> 0, eight faces) or the octahedron split once (detail 1, 32 faces),
        /// pushed out to the ellipsoid's surface. Tree crowns and boulders.
        /// </summary>
        public MeshKit Ellipsoid(Vector3 center, Vector3 radii, int detail = 1)
        {
            if (detail < 0 || detail > 1) throw new System.ArgumentOutOfRangeException(nameof(detail), detail, "an ellipsoid has detail 0 or 1");
            Vector3[] o = { Vector3.up, Vector3.forward, Vector3.right, Vector3.back, Vector3.left, Vector3.down };
            int[,] faces = { { 0, 1, 2 }, { 0, 2, 3 }, { 0, 3, 4 }, { 0, 4, 1 }, { 5, 2, 1 }, { 5, 3, 2 }, { 5, 4, 3 }, { 5, 1, 4 } };
            Vector3 P(Vector3 v) => center + Vector3.Scale(v, radii);
            for (var f = 0; f < 8; f++)
            {
                Vector3 a = o[faces[f, 0]], b = o[faces[f, 1]], c = o[faces[f, 2]];
                if (detail <= 0) { Tri(P(a), P(b), P(c)); continue; }
                Vector3 ab = (a + b).normalized, bc = (b + c).normalized, ca = (c + a).normalized;
                Tri(P(a), P(ab), P(ca));
                Tri(P(ab), P(b), P(bc));
                Tri(P(ca), P(bc), P(c));
                Tri(P(ab), P(bc), P(ca));
            }

            return this;
        }

        /// <summary>
        /// A boat's hull along local +Z (the bow): a square stern, straight sides and a pointed bow, its deck <paramref name="freeboard"/>
        /// above <paramref name="waterline"/> and its keel <paramref name="draft"/> below, narrower at the keel.
        /// </summary>
        public MeshKit Hull(Vector3 waterline, float length, float beam, float freeboard, float draft)
        {
            float hl = length / 2, hb = beam / 2;
            // counter-clockwise from above, starting at the starboard quarter
            Vector2[] plan = { new Vector2(hb, -hl), new Vector2(hb, hl * 0.3f), new Vector2(0, hl), new Vector2(-hb, hl * 0.3f), new Vector2(-hb, -hl) };
            var top = new Vector3[plan.Length]; var bottom = new Vector3[plan.Length];
            for (var i = 0; i < plan.Length; i++)
            {
                top[i] = waterline + new Vector3(plan[i].x, freeboard, plan[i].y);
                bottom[i] = waterline + new Vector3(plan[i].x * 0.55f, -draft, plan[i].y * 0.88f);
            }

            for (var i = 0; i < plan.Length; i++)
            {
                var j = (i + 1) % plan.Length;
                Quad(bottom[i], top[i], top[j], bottom[j]);
            }

            var deck = waterline + new Vector3(0, freeboard, 0);
            var keel = waterline + new Vector3(0, -draft, 0);
            for (var i = 0; i < plan.Length; i++)
            {
                var j = (i + 1) % plan.Length;
                Tri(deck, top[j], top[i]);
                Tri(keel, bottom[i], bottom[j]);
            }

            return this;
        }

        /// <summary>A gabled roof over a footprint centred on <paramref name="center"/> (its eaves), ridge along the local X axis.</summary>
        public MeshKit Gable(Vector3 center, float width, float depth, float rise, float yawDeg = 0)
        {
            var r = Quaternion.Euler(0, yawDeg, 0);
            float hw = width / 2, hd = depth / 2;
            Vector3 P(float x, float y, float z) => center + r * new Vector3(x, y, z);
            Vector3 a = P(-hw, 0, -hd), b = P(hw, 0, -hd), c = P(hw, 0, hd), d = P(-hw, 0, hd), e = P(-hw, rise, 0), f = P(hw, rise, 0);
            Quad(a, e, f, b);
            Quad(c, f, e, d);
            Tri(a, d, e);
            Tri(b, f, c);
            return this;
        }

        /// <summary>A hollow cylindrical section; angles in degrees, with zero along +X.</summary>
        public MeshKit Shell(Vector3 floor, float radius, float height, float thickness, float from = 0, float to = 360, int segments = 32)
        {
            for (var i = 0; i < segments; i++)
            {
                var a = Mathf.Lerp(from, to, i / (float)segments) * Mathf.Deg2Rad;
                var b = Mathf.Lerp(from, to, (i + 1f) / segments) * Mathf.Deg2Rad;
                var da = new Vector3(Mathf.Cos(a), 0, Mathf.Sin(a));
                var db = new Vector3(Mathf.Cos(b), 0, Mathf.Sin(b));
                var up = Vector3.up * height;
                var oa = floor + da * radius; var ob = floor + db * radius;
                var ia = floor + da * (radius - thickness); var ib = floor + db * (radius - thickness);
                Quad(oa, oa + up, ob + up, ob);
                Quad(ib, ib + up, ia + up, ia);
                Quad(ia + up, ib + up, ob + up, oa + up);
                Quad(oa, ob, ib, ia);
                if (i == 0) Quad(ia, ia + up, oa + up, oa);
                if (i == segments - 1) Quad(ob, ob + up, ib + up, ib);
            }
            return this;
        }

        public MeshKit Ring(Vector3 center, float radius, float width = 0.04f) => Shell(center, radius, width, width);

        public MeshKit Rod(Vector3 a, Vector3 b, float radius = 0.04f) => Pipe(a, b - a, radius, (b - a).magnitude, 6);

        /// <summary>Destroys an object the scene made at run time, in Play mode or (for the Editor's stills) in edit mode.</summary>
        public static void Release(Object o)
        {
            if (o == null) return;
            if (Application.isPlaying) Object.Destroy(o);
            else Object.DestroyImmediate(o);
        }

        public Mesh Build(string name)
        {
            var m = new Mesh { name = name };
            if (vertices.Count > 65000) m.indexFormat = IndexFormat.UInt32;
            m.SetVertices(vertices);
            m.SetNormals(normals);
            m.SetTriangles(triangles, 0);
            m.RecalculateBounds();
            return m;
        }

        /// <summary>A GameObject drawing this mesh with one material, under <paramref name="parent"/>.</summary>
        public GameObject Spawn(string name, Transform parent, Material material, bool castShadows = true)
        {
            var go = new GameObject(name);
            go.transform.SetParent(parent, false);
            go.AddComponent<MeshFilter>().sharedMesh = Build(name);
            var mr = go.AddComponent<MeshRenderer>();
            mr.sharedMaterial = material;
            mr.shadowCastingMode = castShadows ? ShadowCastingMode.On : ShadowCastingMode.Off;
            return go;
        }
    }
}
