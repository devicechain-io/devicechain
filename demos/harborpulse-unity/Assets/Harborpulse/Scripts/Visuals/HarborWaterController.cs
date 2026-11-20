// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Collections.Generic;
using DeviceChain.Harborpulse.Model;
using UnityEngine;
using UnityEngine.Rendering;

namespace DeviceChain.Harborpulse.Visuals
{
    /// <summary>
    /// The harbour water: a fine grid that follows the camera (snapped to its own cell, so the waves never swim) for the Gerstner displacement,
    /// and round it a coarse ring out to the horizon with the same shader and no displacement. The waves fade out before the grid's edge
    /// (the shader's fade ends inside it), so the two meet at the same flat tide with no step and never overlap. Each frame it hands the shader the quays' observed tide
    /// levels (from the <see cref="TideBoard"/>) and the waves for the story's storm (from <see cref="GerstnerWaves"/>): the surface is the
    /// observed tide plus zero-mean waves, nothing else.
    /// </summary>
    public sealed class HarborWaterController
    {
        const int GridCells = 360;
        const float Cell = 4f;
        const float RingStep = 250f;

        static readonly int WaveA = Shader.PropertyToID("_WaveA"), WaveB = Shader.PropertyToID("_WaveB"), WaveC = Shader.PropertyToID("_WaveC"),
            WaveD = Shader.PropertyToID("_WaveD"), WaveAmp = Shader.PropertyToID("_WaveAmp"), WaveOmega = Shader.PropertyToID("_WaveOmega");
        static readonly int TideLevels = Shader.PropertyToID("_TideLevels"), TideSpanAB = Shader.PropertyToID("_TideSpanAB"), TideSpanC = Shader.PropertyToID("_TideSpanC");

        /// <summary>The distances over which the shader fades the waves out (its _WaveFadeStart and _WaveFadeEnd defaults).</summary>
        public const float WaveFadeStart = 250, WaveFadeEnd = 680;
        static readonly int WaterTime = Shader.PropertyToID("_WaterTime"), Storm = Shader.PropertyToID("_HarborStorm"), Rain = Shader.PropertyToID("_HarborRain");
        static readonly int Displace = Shader.PropertyToID("_Displace");

        static readonly int WellDiscs = Shader.PropertyToID("_HarborWellDiscs"), WellCount = Shader.PropertyToID("_HarborWellCount");
        readonly Transform near, far;
        readonly List<string> quayOrder = new List<string>();
        readonly double[] xMin = new double[3], xMax = new double[3], levels = new double[3];
        readonly Material nearMat, farMat;
        readonly Mesh nearMesh, farMesh;
        WaveComponent[] waves = GerstnerWaves.For(0);
        float time;
        Vector3 camera;

        /// <param name="plan">The district's three quays and pump footprints; the water orders quays west to east.</param>
        public HarborWaterController(Transform parent, Shader waterShader, SitePlan plan, float farExtent)
        {
            var quays = plan.Quays;
            var discs = new Vector4[3];
            var count = 0;
            foreach (var p in plan.Placements)
            {
                if (p.Device.Kind != DeviceKind.Pump) continue;
                if (count == discs.Length) throw new System.ArgumentException("water mask supports at most three wells");
                var well = new WellFootprint(p.At);
                discs[count++] = new Vector4((float)well.Center.X, (float)well.Center.Z, (float)WellFootprint.RadiusM, 0);
            }
            Shader.SetGlobalVectorArray(WellDiscs, discs);
            Shader.SetGlobalInt(WellCount, count);
            if (quays.Count != 3) throw new System.ArgumentException("the water shader takes exactly three quays; the layout has " + quays.Count);
            var shader = waterShader != null ? waterShader : Shader.Find("Harborpulse/Water");
            if (shader == null) throw new System.InvalidOperationException("the Harborpulse/Water shader is missing from the project");
            var ordered = new List<QuayPlan>(quays);
            ordered.Sort((a, b) => a.CenterX.CompareTo(b.CenterX));
            for (var i = 0; i < 3; i++)
            {
                quayOrder.Add(ordered[i].Token);
                xMin[i] = ordered[i].XMin;
                xMax[i] = ordered[i].XMax;
                if (i > 0 && xMin[i] <= xMax[i - 1]) throw new System.ArgumentException("quays " + ordered[i - 1].Token + " and " + ordered[i].Token + " overlap");
            }

            Shader.SetGlobalVector(TideSpanAB, new Vector4((float)xMin[0], (float)xMax[0], (float)xMin[1], (float)xMax[1]));
            Shader.SetGlobalVector(TideSpanC, new Vector4((float)xMin[2], (float)xMax[2], 0, 0));

            nearMat = new Material(shader) { name = "Harbor/water near" };
            nearMat.SetFloat(Displace, 1);
            farMat = new Material(shader) { name = "Harbor/water far" };
            farMat.SetFloat(Displace, 0);

            var go = new GameObject("Water (near grid)");
            go.transform.SetParent(parent, false);
            go.layer = PlanarWaterReflection.WaterLayer;
            go.AddComponent<MeshFilter>().sharedMesh = nearMesh = Grid(GridCells, Cell);
            var mr = go.AddComponent<MeshRenderer>();
            mr.sharedMaterial = nearMat;
            mr.shadowCastingMode = ShadowCastingMode.Off;
            near = go.transform;

            var ring = new GameObject("Water (far ring)");
            ring.transform.SetParent(parent, false);
            ring.layer = PlanarWaterReflection.WaterLayer;
            ring.AddComponent<MeshFilter>().sharedMesh = farMesh = Ring(GridCells * Cell / 2, farExtent, RingStep);
            var fr = ring.AddComponent<MeshRenderer>();
            fr.sharedMaterial = farMat;
            fr.shadowCastingMode = ShadowCastingMode.Off;
            far = ring.transform;
        }

        /// <summary>A flat square ring: the square of half-side <paramref name="outer"/> less the square of half-side <paramref name="inner"/>.</summary>
        static Mesh Ring(float inner, float outer, float step)
        {
            var vertices = new List<Vector3>();
            var tris = new List<int>();
            void Rect(float x0, float x1, float z0, float z1)
            {
                int nx = Mathf.Max(1, Mathf.CeilToInt((x1 - x0) / step)), nz = Mathf.Max(1, Mathf.CeilToInt((z1 - z0) / step));
                var start = vertices.Count;
                for (var z = 0; z <= nz; z++)
                for (var x = 0; x <= nx; x++)
                    vertices.Add(new Vector3(Mathf.Lerp(x0, x1, x / (float)nx), 0, Mathf.Lerp(z0, z1, z / (float)nz)));
                for (var z = 0; z < nz; z++)
                for (var x = 0; x < nx; x++)
                {
                    var i = start + z * (nx + 1) + x;
                    tris.Add(i);
                    tris.Add(i + nx + 1);
                    tris.Add(i + nx + 2);
                    tris.Add(i);
                    tris.Add(i + nx + 2);
                    tris.Add(i + 1);
                }
            }

            Rect(-outer, outer, inner, outer);
            Rect(-outer, outer, -outer, -inner);
            Rect(-outer, -inner, -inner, inner);
            Rect(inner, outer, -inner, inner);
            var m = new Mesh { name = "water ring", indexFormat = vertices.Count > 65000 ? IndexFormat.UInt32 : IndexFormat.UInt16 };
            m.SetVertices(vertices);
            m.SetTriangles(tris, 0);
            m.RecalculateNormals();
            m.bounds = new Bounds(Vector3.zero, new Vector3(outer * 2 + 20, 40, outer * 2 + 20));
            return m;
        }

        /// <summary>A flat grid centred on the origin, <paramref name="cells"/> square cells of <paramref name="size"/> metres a side.</summary>
        static Mesh Grid(int cells, float size)
        {
            var n = cells + 1;
            var vertices = new Vector3[n * n];
            var half = cells * size / 2;
            for (var z = 0; z < n; z++)
            for (var x = 0; x < n; x++)
                vertices[z * n + x] = new Vector3(x * size - half, 0, z * size - half);
            var tris = new int[cells * cells * 6];
            var t = 0;
            for (var z = 0; z < cells; z++)
            for (var x = 0; x < cells; x++)
            {
                var i = z * n + x;
                tris[t++] = i;
                tris[t++] = i + n;
                tris[t++] = i + n + 1;
                tris[t++] = i;
                tris[t++] = i + n + 1;
                tris[t++] = i + 1;
            }

            var m = new Mesh { name = "water grid", indexFormat = vertices.Length > 65000 ? IndexFormat.UInt32 : IndexFormat.UInt16 };
            m.vertices = vertices;
            m.triangles = tris;
            m.RecalculateNormals();
            // the shader moves vertices by up to a few metres; keep the grid from being culled at the frustum's edge
            m.bounds = new Bounds(Vector3.zero, new Vector3(cells * size + 20, 40, cells * size + 20));
            return m;
        }

        /// <summary>
        /// Hands the shader this frame's water. <paramref name="levelOf"/> is each quay's level to draw, already decided by the caller (the
        /// observed median, or the held one); <paramref name="fallback"/> is used for a quay with none.
        /// </summary>
        public void Apply(System.Func<string, double?> levelOf, double fallback, Weather weather, float waterTime, Vector3 cameraPos)
        {
            time = waterTime;
            double sum = 0;
            for (var i = 0; i < 3; i++)
            {
                var l = levelOf(quayOrder[i]) ?? fallback;
                levels[i] = l;
                sum += l;
            }

            waves = GerstnerWaves.For(weather.Storm);
            camera = cameraPos;
            Shader.SetGlobalVector(TideLevels, new Vector4((float)levels[0], (float)levels[1], (float)levels[2], (float)(sum / 3)));
            Shader.SetGlobalVector(WaveA, Pack(waves[0]));
            Shader.SetGlobalVector(WaveB, Pack(waves[1]));
            Shader.SetGlobalVector(WaveC, Pack(waves[2]));
            Shader.SetGlobalVector(WaveD, Pack(waves[3]));
            Shader.SetGlobalVector(WaveAmp, new Vector4((float)waves[0].AmplitudeM, (float)waves[1].AmplitudeM, (float)waves[2].AmplitudeM, (float)waves[3].AmplitudeM));
            Shader.SetGlobalVector(WaveOmega, new Vector4((float)waves[0].Omega, (float)waves[1].Omega, (float)waves[2].Omega, (float)waves[3].Omega));
            Shader.SetGlobalFloat(WaterTime, waterTime);
            Shader.SetGlobalFloat(Storm, (float)weather.Storm);
            Shader.SetGlobalFloat(Rain, (float)weather.Rain);

            // the grid follows the camera in whole cells
            var snapped = new Vector3(Mathf.Round(cameraPos.x / Cell) * Cell, 0, Mathf.Round(cameraPos.z / Cell) * Cell);
            near.position = snapped;
            far.position = snapped;
        }

        static Vector4 Pack(WaveComponent w) => new Vector4((float)w.DirX, (float)w.DirZ, (float)w.Steepness, (float)w.K);

        /// <summary>
        /// The water surface's height at a rest position, as the shader draws it there this frame: the quay's tide (TideProfile, the shader's
        /// HarborTide) plus the waves, faded with the distance from the camera exactly as the shader fades them.
        /// </summary>
        public float SurfaceAt(float x, float z)
        {
            var tide = TideProfile.LevelAt(x, xMin, xMax, levels);
            var dist = Vector2.Distance(new Vector2(x, z), new Vector2(camera.x, camera.z));
            var fade = 1 - Mathf.SmoothStep(0, 1, Mathf.InverseLerp(WaveFadeStart, WaveFadeEnd, dist));
            GerstnerWaves.Displace(waves, x, z, time, out _, out var dy, out _);
            return (float)(tide + dy * fade);
        }

        /// <summary>Destroys the meshes and materials this controller made.</summary>
        public void Dispose()
        {
            Shader.SetGlobalInt(WellCount, 0);
            MeshKit.Release(nearMat);
            MeshKit.Release(farMat);
            MeshKit.Release(nearMesh);
            MeshKit.Release(farMesh);
            MeshKit.Release(near.gameObject);
            MeshKit.Release(far.gameObject);
        }
    }
}
