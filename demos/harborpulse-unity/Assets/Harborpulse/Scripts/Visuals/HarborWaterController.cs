// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Collections.Generic;
using DeviceChain.Harborpulse.Model;
using DeviceChain.Harborpulse.Model.Director;
using System.Linq;
using UnityEngine;
using UnityEngine.Rendering;

namespace DeviceChain.Harborpulse.Visuals
{
    /// <summary>
    /// The harbour water: a fine grid that follows the camera (snapped to its own cell, so the waves never swim) for the Gerstner displacement,
        /// and round it a coast-clipped ring out to the horizon with the same shader and no displacement. The waves fade out before the grid's edge
    /// (the shader's fade ends inside it), so the two meet at the same flat tide with no step and never overlap. Each frame it hands the shader the quays' observed tide
    /// levels (from the <see cref="TideBoard"/>) and the waves for the story's storm (from <see cref="GerstnerWaves"/>): the surface is the
    /// observed tide plus zero-mean waves, nothing else.
    /// </summary>
    public sealed class HarborWaterController
    {
        const int GridCells = 360;
        const float Cell = 4f;

        static readonly int WaveA = Shader.PropertyToID("_WaveA"), WaveB = Shader.PropertyToID("_WaveB"), WaveC = Shader.PropertyToID("_WaveC"),
            WaveD = Shader.PropertyToID("_WaveD"), WaveAmp = Shader.PropertyToID("_WaveAmp"), WaveOmega = Shader.PropertyToID("_WaveOmega");
        static readonly int TideLevels = Shader.PropertyToID("_TideLevels"), TideRects = Shader.PropertyToID("_TideRects");

        /// <summary>The distances over which the shader fades the waves out (its _WaveFadeStart and _WaveFadeEnd defaults).</summary>
        public const float WaveFadeStart = 250, WaveFadeEnd = 680;
        static readonly int WaterTime = Shader.PropertyToID("_WaterTime"), Storm = Shader.PropertyToID("_HarborStorm"), Rain = Shader.PropertyToID("_HarborRain");
        static readonly int Displace = Shader.PropertyToID("_Displace");

        static readonly int WellDiscs = Shader.PropertyToID("_HarborWellDiscs"), WellCount = Shader.PropertyToID("_HarborWellCount");
        readonly Transform near, far;
        readonly List<string> quayOrder = new List<string>();
        readonly double[] levels = new double[3];
        readonly TideField tideField;
        readonly Material nearMat, farMat;
        readonly Mesh nearMesh, farMesh;
        readonly OceanPatch[] oceanPatches = new OceanPatch[OceanRing.PatchCount];
        readonly Vector3[] oceanVertices = new Vector3[OceanRing.PatchCount * 4];
        readonly float farExtent;
        Vector3 ringCenter = new Vector3(float.NaN, 0, float.NaN);
        WaveComponent[] waves = GerstnerWaves.For(0);
        float time;
        Vector3 camera;

        /// <param name="plan">The district's three quays and pump footprints; the water orders quays west to east.</param>
        public HarborWaterController(Transform parent, Shader waterShader, PresentationMap map, HarborWorld world, float farExtent)
        {
            this.farExtent = farExtent;
            var regions = new List<TideField.Region>();
            var rects = new Vector4[3];
            var index = 0;
            foreach (var q in map.Quays.OrderBy(q => q.Key, System.StringComparer.Ordinal))
            {
                quayOrder.Add(q.Key); var r = new TideField.Region(q.Value); regions.Add(r);
                rects[index++] = new Vector4((float)r.MinX, (float)r.MinZ, (float)r.MaxX, (float)r.MaxZ);
            }
            tideField = new TideField(regions);
            Shader.SetGlobalVectorArray(TideRects, rects);
            var discs = new Vector4[3]; var count = 0;
            foreach (var rig in world.Rigs)
            {
                if (rig.Placement.Device.Kind != DeviceKind.Pump) continue;
                var well = rig.Root.TransformPoint(new Vector3((float)WellFootprint.LocalX, 0, (float)WellFootprint.LocalZ));
                discs[count++] = new Vector4(well.x, well.z, (float)WellFootprint.RadiusM, 0);
            }
            Shader.SetGlobalVectorArray(WellDiscs, discs); Shader.SetGlobalInt(WellCount, count);
            var shader = waterShader != null ? waterShader : Shader.Find("Harborpulse/Water");
            if (shader == null) throw new System.InvalidOperationException("Harborpulse/Water shader missing");

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
            ring.AddComponent<MeshFilter>().sharedMesh = farMesh = Ring();
            var fr = ring.AddComponent<MeshRenderer>();
            fr.sharedMaterial = farMat;
            fr.shadowCastingMode = ShadowCastingMode.Off;
            far = ring.transform;
            UpdateRing(Vector3.zero);
        }

        /// <summary>Eight fixed patch slots for the coast-clipped square ring. All vertices remain in WORLD coordinates.</summary>
        Mesh Ring()
        {
            var normals = new Vector3[oceanVertices.Length];
            var tris = new int[OceanRing.PatchCount * 6];
            for(var p=0;p<OceanRing.PatchCount;p++)
            {
                var i=p*4;var t=p*6;
                for(var k=0;k<4;k++)normals[i+k]=Vector3.up;
                tris[t]=i;tris[t+1]=i+1;tris[t+2]=i+2;
                tris[t+3]=i;tris[t+4]=i+2;tris[t+5]=i+3;
            }
            var m = new Mesh { name = "coast-clipped water ring" };
            m.MarkDynamic();m.vertices=oceanVertices;m.normals=normals;m.triangles=tris;
            return m;
        }

        void UpdateRing(Vector3 center)
        {
            if(center==ringCenter)return;
            ringCenter=center;
            OceanRing.Fill(center.x,center.z,GridCells*Cell/2,farExtent,oceanPatches);
            for(var p=0;p<OceanRing.PatchCount;p++)
            {
                var r=oceanPatches[p];var i=p*4;
                oceanVertices[i]=new Vector3((float)r.MinX,0,(float)r.MinZ);
                oceanVertices[i+1]=new Vector3((float)r.MinX,0,(float)r.MaxZ);
                oceanVertices[i+2]=new Vector3((float)r.MaxX,0,(float)r.MaxZ);
                oceanVertices[i+3]=new Vector3((float)r.MaxX,0,(float)r.MinZ);
            }
            farMesh.vertices=oceanVertices;
            farMesh.bounds=new Bounds(center,new Vector3(farExtent*2+20,40,farExtent*2+20));
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
            UpdateRing(snapped); // WORLD coast clipping; the mesh itself stays at the origin.
        }

        static Vector4 Pack(WaveComponent w) => new Vector4((float)w.DirX, (float)w.DirZ, (float)w.Steepness, (float)w.K);

        /// <summary>
        /// The water surface's height at a rest position, as the shader draws it there this frame: the quay's tide (TideProfile, the shader's
        /// HarborTide) plus the waves, faded with the distance from the camera exactly as the shader fades them.
        /// </summary>
        public float SurfaceAt(float x, float z)
        {
            var tide = tideField.LevelAt(x, z, levels);
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
