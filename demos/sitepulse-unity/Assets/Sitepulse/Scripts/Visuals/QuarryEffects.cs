// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Collections.Generic;
using UnityEngine;
using UnityEngine.Rendering;

namespace DeviceChain.Sitepulse.Visuals
{
    /// <summary>
    /// Something in the scene that animates cosmetic effects (dust, exhaust, falling material,
    /// beacons, moving belts) from the state of the site. <see cref="Step"/> is called once per
    /// frame after the machines are posed; in the Editor a still is made by stepping the scene
    /// forward in fixed steps with <c>simulate</c> set, so the particles have a history.
    /// </summary>
    public interface IQuarryEffect
    {
        void Step(float dt, bool simulate);
        void ClearParticles();
    }

    /// <summary>
    /// The shared look and budget of the scene's cosmetic effects: the materials and the rock
    /// mesh they draw with, and how much they may emit. Effects are local presentation only:
    /// they show what the machines are doing and are never evidence of platform state.
    /// </summary>
    [CreateAssetMenu(menuName = "Sitepulse/Quarry Effects")]
    public sealed class QuarryEffects : ScriptableObject
    {
        [Tooltip("Soft, lit puffs: dust and haze.")]
        public Material dust;
        [Tooltip("Soft, lit, darker puffs: exhaust.")]
        public Material exhaust;
        [Tooltip("Lit opaque material for falling rock, drawn as meshes.")]
        public Material rock;
        [Tooltip("Additive glow for the rotating beacons.")]
        public Material flare;
        [Tooltip("A low-poly rock, the shape of every falling chunk.")]
        public Mesh chunk;
        [Tooltip("Scales every emission rate and particle limit.")]
        [Range(0f, 1f)] public float budget = 1f;

        /// <summary>The quality level whose name selects the lighter effects budget.</summary>
        public const string LaptopQuality = "Laptop";

        /// <summary>The budget in effect: halved on the Laptop quality level.</summary>
        public float Budget
        {
            get
            {
                int q = QualitySettings.GetQualityLevel();
                var names = QualitySettings.names;
                bool laptop = q >= 0 && q < names.Length && names[q] == LaptopQuality;
                return budget * (laptop ? 0.5f : 1f);
            }
        }

        /// <summary>Every effect currently enabled, for stepping the scene in the Editor.</summary>
        public static readonly List<IQuarryEffect> Active = new List<IQuarryEffect>();

        /// <summary>A particle system under <paramref name="parent"/>, simulated in world space,
        /// emitting nothing until its rate is set. Never saved with the scene.</summary>
        public static ParticleSystem Emitter(Transform parent, string name, Vector3 localPosition, Material material,
                                             int maxParticles, Mesh mesh = null)
        {
            var go = new GameObject(name) { hideFlags = HideFlags.DontSave };
            go.transform.SetParent(parent, false);
            go.transform.localPosition = localPosition;
            var ps = go.AddComponent<ParticleSystem>();
            ps.Stop(true, ParticleSystemStopBehavior.StopEmittingAndClear);
            ps.useAutoRandomSeed = false;
            ps.randomSeed = SeedFor(parent.name, name);
            var main = ps.main;
            main.playOnAwake = false;
            main.loop = true;
            main.simulationSpace = ParticleSystemSimulationSpace.World;
            main.scalingMode = ParticleSystemScalingMode.Shape;
            main.maxParticles = Mathf.Max(1, maxParticles);
            var em = ps.emission;
            em.rateOverTime = 0f;
            var r = go.GetComponent<ParticleSystemRenderer>();
            r.sharedMaterial = material;
            r.shadowCastingMode = ShadowCastingMode.Off;
            r.receiveShadows = false;
            r.sortingFudge = 0f;
            if (mesh != null)
            {
                r.renderMode = ParticleSystemRenderMode.Mesh;
                r.mesh = mesh;
                r.alignment = ParticleSystemRenderSpace.World;
            }
            else
            {
                r.renderMode = ParticleSystemRenderMode.Billboard;
                r.sortMode = ParticleSystemSortMode.Distance;
                r.maxParticleSize = 0.6f;
            }
            if (Application.isPlaying) ps.Play();
            return ps;
        }

        /// <summary>The seed an emitter is made with: a function of where it is, so the same scene always rolls the same dice.</summary>
        public static uint SeedFor(string parentName, string name) => (uint)(Mathf.Abs((parentName + "/" + name).GetHashCode()) + 1);

        /// <summary>
        /// Back to the state the emitter was made in: stopped, empty and on its own seed again. A render jumps to each shot, and what the
        /// shot shows must not depend on which shots were rendered before it (a cleared system keeps its place in the random sequence).
        /// </summary>
        public static void Restart(ParticleSystem ps)
        {
            if (ps == null) return;
            ps.Stop(true, ParticleSystemStopBehavior.StopEmittingAndClear);
            ps.Clear(true);
            var parent = ps.transform.parent;
            ps.useAutoRandomSeed = false;
            ps.randomSeed = SeedFor(parent != null ? parent.name : "", ps.gameObject.name);
        }

        /// <summary>Soft dust: grows, fades in and out, drifts up a little.</summary>
        public static void AsDust(ParticleSystem ps, Color color, Vector2 size, Vector2 life, float alpha, float rise = 0.03f)
        {
            var main = ps.main;
            main.startLifetime = new ParticleSystem.MinMaxCurve(life.x, life.y);
            main.startSpeed = new ParticleSystem.MinMaxCurve(0.2f, 0.9f);
            main.startSize = new ParticleSystem.MinMaxCurve(size.x, size.y);
            main.startRotation = new ParticleSystem.MinMaxCurve(0f, Mathf.PI * 2f);
            main.startColor = color;
            main.gravityModifier = -rise;
            var sol = ps.sizeOverLifetime;
            sol.enabled = true;
            sol.size = new ParticleSystem.MinMaxCurve(1f, AnimationCurve.EaseInOut(0f, 0.55f, 1f, 1.8f));
            var col = ps.colorOverLifetime;
            col.enabled = true;
            var g = new Gradient();
            g.SetKeys(new[] { new GradientColorKey(Color.white, 0f), new GradientColorKey(Color.white, 1f) },
                      new[] { new GradientAlphaKey(0f, 0f), new GradientAlphaKey(alpha, 0.18f), new GradientAlphaKey(alpha * 0.6f, 0.6f), new GradientAlphaKey(0f, 1f) });
            col.color = g;
            var rol = ps.rotationOverLifetime;
            rol.enabled = true;
            rol.z = new ParticleSystem.MinMaxCurve(-0.4f, 0.4f);
            var lim = ps.limitVelocityOverLifetime;
            lim.enabled = true;
            lim.drag = 0.8f;
            lim.multiplyDragByParticleSize = false;
        }

        /// <summary>Falling rock: tumbling mesh chunks under gravity.</summary>
        public static void AsChunks(ParticleSystem ps, Vector2 size, float life, Vector2 speed)
        {
            var main = ps.main;
            main.startLifetime = new ParticleSystem.MinMaxCurve(life * 0.85f, life);
            main.startSpeed = new ParticleSystem.MinMaxCurve(speed.x, speed.y);
            main.startSize3D = false;
            main.startSize = new ParticleSystem.MinMaxCurve(size.x, size.y);
            main.startRotation3D = true;
            main.startRotationX = new ParticleSystem.MinMaxCurve(0f, Mathf.PI * 2f);
            main.startRotationY = new ParticleSystem.MinMaxCurve(0f, Mathf.PI * 2f);
            main.startRotationZ = new ParticleSystem.MinMaxCurve(0f, Mathf.PI * 2f);
            main.gravityModifier = 1f;
            main.startColor = Color.white;
            var rol = ps.rotationOverLifetime;
            rol.enabled = true;
            rol.separateAxes = true;
            rol.x = new ParticleSystem.MinMaxCurve(-3f, 3f);
            rol.y = new ParticleSystem.MinMaxCurve(-3f, 3f);
            rol.z = new ParticleSystem.MinMaxCurve(-3f, 3f);
        }

        /// <summary>A box-shaped emission area (local to the emitter).</summary>
        public static void Box(ParticleSystem ps, Vector3 size, Vector3 euler = default)
        {
            var sh = ps.shape;
            sh.enabled = true;
            sh.shapeType = ParticleSystemShapeType.Box;
            sh.scale = size;
            sh.rotation = euler;
            sh.randomDirectionAmount = 0.15f;
        }

        /// <summary>A narrow cone (emits along its local +Z, here pointed by <paramref name="euler"/>).</summary>
        public static void Cone(ParticleSystem ps, float angle, float radius, Vector3 euler)
        {
            var sh = ps.shape;
            sh.enabled = true;
            sh.shapeType = ParticleSystemShapeType.Cone;
            sh.angle = angle;
            sh.radius = radius;
            sh.rotation = euler;
        }

        public static void Rate(ParticleSystem ps, float perSecond)
        {
            if (ps == null) return;
            var em = ps.emission;
            em.rateOverTime = Mathf.Max(0f, perSecond);
        }

        public static void Advance(ParticleSystem ps, float dt, bool simulate)
        {
            if (ps == null) return;
            if (simulate)
            {
                // a stopped system does not emit while it is simulated: start it first
                if (!ps.isEmitting) ps.Play(true);
                ps.Simulate(dt, true, false, false);
            }
            else if (Application.isPlaying && !ps.isPlaying) ps.Play();
        }

        /// <summary>A camera-facing quad for a glow, with its own property block.</summary>
        public static MeshRenderer Flare(Transform parent, string name, Material material)
        {
            var go = new GameObject(name) { hideFlags = HideFlags.DontSave };
            go.transform.SetParent(parent, false);
            go.AddComponent<MeshFilter>().sharedMesh = QuadMesh;
            var mr = go.AddComponent<MeshRenderer>();
            mr.sharedMaterial = material;
            mr.shadowCastingMode = ShadowCastingMode.Off;
            mr.receiveShadows = false;
            return mr;
        }

        static Mesh quad;

        static Mesh QuadMesh
        {
            get
            {
                if (quad != null) return quad;
                quad = new Mesh { name = "FlareQuad", hideFlags = HideFlags.DontSave };
                quad.SetVertices(new List<Vector3> { new(-0.5f, -0.5f, 0), new(0.5f, -0.5f, 0), new(-0.5f, 0.5f, 0), new(0.5f, 0.5f, 0) });
                quad.SetUVs(0, new List<Vector2> { new(0, 0), new(1, 0), new(0, 1), new(1, 1) });
                quad.SetColors(new List<Color> { Color.white, Color.white, Color.white, Color.white });
                quad.SetTriangles(new[] { 0, 2, 1, 1, 2, 3 }, 0);
                quad.RecalculateBounds();
                return quad;
            }
        }
    }
}
