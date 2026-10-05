// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Collections.Generic;
using UnityEngine;
using UnityEngine.Rendering;

namespace DeviceChain.Sitepulse.Visuals
{
    /// <summary>
    /// The refuel bay at work: while a haul truck stands in the bay, a fuel attendant holds the
    /// dispenser's hose to the truck's fast-fill coupling; otherwise the attendant waits by the
    /// dispenser. The hose hangs from the dispenser's outlet to the coupling. Local presentation
    /// only, driven by where the preview choreography has put the truck.
    /// </summary>
    [ExecuteAlways]
    public sealed class RefuelVignette : MonoBehaviour, IQuarryEffect
    {
        public QuarryFleetPreview fleet;
        public QuarryTerrain terrain;
        [Tooltip("The fuel tank prop whose dispenser serves the bay.")]
        public Transform fuelTank;
        [Tooltip("The attendant (the worker prefab).")]
        public GameObject worker;
        public Material hoseMaterial;
        [Tooltip("The bay: where a truck stands to be fuelled (x, z).")]
        public Vector2 bay = new Vector2(-59.5f, -56.5f);

        // the dispenser's hose outlet, in the fuel tank prop's frame (ArtSource/props/build_props.py)
        static readonly Vector3 Outlet = new Vector3(-0.9f, 1.5f, 3.2f);
        // the haul truck's fast-fill coupling on the outside of its fuel tank, in its frame
        static readonly Vector3 Coupling = new Vector3(-1.5f, 1.36f, -0.2f);
        // the worker's right hand, in its frame (it faces +Z)
        static readonly Vector3 Hand = new Vector3(0.3f, 1.36f, 0.6f);

        GameObject attendant, hose;
        Mesh hoseMesh;
        Vector3 lastA, lastB;

        void OnEnable() => QuarryEffects.Active.Add(this);

        void OnDisable()
        {
            QuarryEffects.Active.Remove(this);
            if (attendant != null) DestroyImmediate(attendant);
            if (hose != null) DestroyImmediate(hose);
            if (hoseMesh != null) DestroyImmediate(hoseMesh);
            attendant = hose = null;
        }

        void Update()
        {
            if (Application.isPlaying) Step(Time.deltaTime, false);
        }

        public void ClearParticles() { }

        public void Step(float dt, bool simulate)
        {
            if (fleet == null || fuelTank == null || worker == null || terrain == null || !terrain.Built) return;
            if (attendant == null)
            {
                attendant = Instantiate(worker, transform);
                attendant.name = "Fuel attendant";
                foreach (var t in attendant.GetComponentsInChildren<Transform>(true)) t.gameObject.hideFlags = HideFlags.DontSave;
                hose = new GameObject("Fuel hose") { hideFlags = HideFlags.DontSave };
                hose.transform.SetParent(transform, false);
                hoseMesh = new Mesh { name = "FuelHose", hideFlags = HideFlags.DontSave };
                lastA = lastB = new Vector3(float.NaN, 0f, 0f);
                hose.AddComponent<MeshFilter>().sharedMesh = hoseMesh;
                var mr = hose.AddComponent<MeshRenderer>();
                mr.sharedMaterial = hoseMaterial;
                mr.shadowCastingMode = ShadowCastingMode.On;
            }
            MachineRig truck = null;
            foreach (var m in fleet.Machines)
            {
                if (m.Kind != MachineKind.Hauler) continue;
                var p = m.transform.position;
                if (new Vector2(p.x - bay.x, p.z - bay.y).sqrMagnitude < 0.6f) truck = m;
            }
            var outlet = fuelTank.TransformPoint(Outlet);
            if (truck != null)
            {
                // at the truck's side, facing it, holding the nozzle on the coupling
                var c = truck.transform.TransformPoint(Coupling);
                var face = Quaternion.LookRotation(Vector3.ProjectOnPlane(truck.transform.right, Vector3.up), Vector3.up);
                var at = c - face * new Vector3(Hand.x, 0f, Hand.z);
                at.y = terrain.HeightAt(at.x, at.z);
                attendant.transform.SetPositionAndRotation(at, face);
                hose.SetActive(true);
                Hang(outlet, attendant.transform.TransformPoint(Hand));
            }
            else
            {
                // waiting beside the dispenser, facing the bay
                var at = fuelTank.TransformPoint(new Vector3(0.4f, 0f, 3.6f));
                at.y = terrain.HeightAt(at.x, at.z);
                var face = Quaternion.LookRotation(Vector3.ProjectOnPlane(fuelTank.forward, Vector3.up), Vector3.up);
                attendant.transform.SetPositionAndRotation(at, face);
                hose.SetActive(false);
            }
        }

        /// <summary>The hose: a tube hanging between a and b, sagging under its weight.</summary>
        void Hang(Vector3 a, Vector3 b)
        {
            if ((a - lastA).sqrMagnitude < 1e-6f && (b - lastB).sqrMagnitude < 1e-6f && hoseMesh.vertexCount > 0) return;
            lastA = a;
            lastB = b;
            const int n = 20, sides = 6;
            const float r = 0.04f;
            float span = Vector3.Distance(a, b);
            float floor = Mathf.Min(terrain.HeightAt((a.x + b.x) / 2f, (a.z + b.z) / 2f) + r, Mathf.Min(a.y, b.y));
            var pts = new Vector3[n + 1];
            for (int i = 0; i <= n; i++)
            {
                float u = i / (float)n;
                var p = Vector3.Lerp(a, b, u);
                p.y = Mathf.Max(floor, p.y - 0.45f * span * 4f * u * (1f - u) * 0.5f);
                pts[i] = p;
            }
            var v = new List<Vector3>();
            var nrm = new List<Vector3>();
            var tri = new List<int>();
            for (int i = 0; i <= n; i++)
            {
                var dir = (pts[Mathf.Min(i + 1, n)] - pts[Mathf.Max(i - 1, 0)]).normalized;
                var side = Vector3.Cross(dir, Vector3.up).normalized;
                if (side.sqrMagnitude < 1e-4f) side = Vector3.right;
                var up = Vector3.Cross(side, dir);
                for (int k = 0; k < sides; k++)
                {
                    float ang = k * Mathf.PI * 2f / sides;
                    var o = side * Mathf.Cos(ang) + up * Mathf.Sin(ang);
                    v.Add(transform.InverseTransformPoint(pts[i] + o * r));
                    nrm.Add(transform.InverseTransformDirection(o));
                    if (i < n)
                    {
                        int k1 = (k + 1) % sides, b0 = i * sides;
                        tri.AddRange(new[] { b0 + k, b0 + sides + k, b0 + k1, b0 + k1, b0 + sides + k, b0 + sides + k1 });
                    }
                }
            }
            hoseMesh.Clear();
            hoseMesh.SetVertices(v);
            hoseMesh.SetNormals(nrm);
            hoseMesh.SetTriangles(tri, 0);
            hoseMesh.RecalculateBounds();
        }
    }
}
