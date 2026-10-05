// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Collections.Generic;
using System.Linq;
using NUnit.Framework;
using UnityEditor;
using UnityEngine;

namespace DeviceChain.Demos.Tests
{
    public class IntroTimelineTests
    {
        const float Frame = 1f / 60f;
        static float Angle(Quaternion q) => Quaternion.Angle(q, Quaternion.identity);

        [Test]
        public void BeatsRunInOrderAndLastAboutThreeAndAHalfSeconds()
        {
            Assert.That(IntroTimeline.TraceStart, Is.LessThan(IntroTimeline.TraceEnd));
            Assert.That(IntroTimeline.TraceEnd, Is.LessThan(IntroTimeline.Lock));
            Assert.That(IntroTimeline.SweepEnd, Is.LessThan(IntroTimeline.Lock));
            Assert.That(IntroTimeline.FrameSettled, Is.LessThanOrEqualTo(IntroTimeline.Lock));
            Assert.That(IntroTimeline.Lock, Is.LessThan(IntroTimeline.WordmarkIn));
            Assert.That(IntroTimeline.WordmarkFull, Is.LessThan(IntroTimeline.DissolveStart));
            Assert.That(IntroTimeline.DissolveStart - IntroTimeline.WordmarkFull, Is.GreaterThanOrEqualTo(0.8f - 1e-4f), "the full lockup holds for 0.8 s");
            Assert.That(IntroTimeline.WordmarkGone, Is.LessThan(IntroTimeline.End));
            Assert.That(IntroTimeline.End, Is.InRange(3.4f, 3.8f));
        }

        [Test]
        public void OpensOnALitBackgroundAndTheRimStartsAtOnce()
        {
            var start = IntroTimeline.Evaluate(0f);
            Assert.That(start.backdrop, Is.EqualTo(0.3f).Within(1e-4f), "not pure black");
            Assert.That(IntroTimeline.TraceStart, Is.EqualTo(0f));
            Assert.That(IntroTimeline.Evaluate(0.1f).trace, Is.GreaterThan(0f));
            Assert.That(IntroTimeline.Evaluate(0.5f).light, Is.GreaterThan(0.5f), "the cube is lit, not a dark shape, by half a second");
            Assert.That(start.wordmark, Is.EqualTo(0f));
        }

        [Test]
        public void LocksExactlyIntoTheIsometricPoseWithFlatBrandColours()
        {
            var p = IntroTimeline.Evaluate(IntroTimeline.Lock);
            Assert.That(Angle(p.frameRotation), Is.LessThan(1e-3f));
            Assert.That(Angle(p.cubeRotation), Is.LessThan(1e-3f));
            Assert.That(p.cubeScale, Is.EqualTo(1f));
            Assert.That(p.flat, Is.EqualTo(1f));
            Assert.That(p.chamfer, Is.EqualTo(0f), "the drawing's sharp edges");
            Assert.That(p.bloom, Is.EqualTo(0f), "no bloom on the brand colours");
            // and it stays exact from the settle through the hold
            for (float t = IntroTimeline.Settled; t <= IntroTimeline.DissolveStart; t += Frame)
            {
                var q = IntroTimeline.Evaluate(t);
                Assert.That(Angle(q.frameRotation) + Angle(q.cubeRotation), Is.LessThan(1e-3f), $"t={t}");
                Assert.That(q.flat, Is.EqualTo(1f), $"t={t}");
                Assert.That(q.chamfer + q.glow + q.cubeGlow + q.bloom + q.packets, Is.EqualTo(0f), $"t={t}: nothing on the locked mark");
                Assert.That(q.holeScale, Is.EqualTo(1f), $"t={t}");
                Assert.That(q.cubeAlpha, Is.EqualTo(1f), $"t={t}");
            }
        }

        [Test]
        public void TheCubeLandsWithWeightAndSettles()
        {
            // still moving well into the lock: no dead air before it
            float speed = (IntroTimeline.CubeAngle(IntroTimeline.Lock - 2f * Frame) - IntroTimeline.CubeAngle(IntroTimeline.Lock - Frame)) / Frame;
            Assert.That(speed, Is.GreaterThan(150f), "degrees a second just before the lock");
            for (float t = IntroTimeline.SpinStart + Frame; t < IntroTimeline.Lock; t += Frame)
                Assert.That(IntroTimeline.CubeAngle(t - Frame) - IntroTimeline.CubeAngle(t), Is.GreaterThan(2.5f), $"t={t}: turning at least 2.5 degrees a frame");
            // it carries past the pose by a few degrees and comes back
            float overshoot = 0f;
            for (float t = IntroTimeline.Lock; t < IntroTimeline.Settled; t += 0.001f)
                overshoot = Mathf.Min(overshoot, IntroTimeline.CubeAngle(t));
            Assert.That(-overshoot, Is.InRange(3.5f, 5.5f));
            // continuous through the lock, and exact from the settle on
            Assert.That(Mathf.Abs(IntroTimeline.CubeAngle(IntroTimeline.Lock - 1e-4f) - IntroTimeline.CubeAngle(IntroTimeline.Lock + 1e-4f)), Is.LessThan(0.1f));
            Assert.That(IntroTimeline.CubeAngle(IntroTimeline.Settled), Is.EqualTo(0f));
            Assert.That(Angle(IntroTimeline.Evaluate(IntroTimeline.Lock + 0.05f).cubeRotation), Is.GreaterThan(2f), "the overshoot shows");
        }

        /// <summary>The spin's direction about the view axis between two moments: + is about +Z.</summary>
        static float SpinZ(Quaternion from, Quaternion to)
        {
            (to * Quaternion.Inverse(from)).ToAngleAxis(out float angle, out var axis);
            return angle * axis.z;
        }

        [Test]
        public void TheFrameTiltsAndTurnsAgainstTheCubeThenSettles()
        {
            var early = IntroTimeline.Evaluate(IntroTimeline.SpinStart);
            Assert.That(Angle(early.frameRotation), Is.GreaterThan(IntroTimeline.FrameTilt));
            // the frame is tilted about X, so its depth shows, and the tilt eases out
            Vector3 normal = early.frameRotation * Vector3.forward;
            Assert.That(Vector3.Angle(normal, Vector3.forward), Is.EqualTo(IntroTimeline.FrameTilt).Within(0.01f));
            for (float t = 0.3f; t < 1.2f; t += 0.3f)
            {
                var a = IntroTimeline.Evaluate(t);
                var b = IntroTimeline.Evaluate(t + Frame);
                float frame = SpinZ(a.frameRotation, b.frameRotation);
                float cube = SpinZ(a.cubeRotation, b.cubeRotation);
                Assert.That(frame * cube, Is.LessThan(0f), $"t={t}: frame {frame}, cube {cube} degrees about Z");
            }
            Assert.That(Angle(IntroTimeline.Evaluate(IntroTimeline.FrameSettled).frameRotation), Is.LessThan(1e-3f));
        }

        [Test]
        public void TheColoursEaseInAsTheCubeArrives()
        {
            float start = IntroTimeline.Lock - IntroTimeline.FlattenLead;
            Assert.That(IntroTimeline.FlattenLead, Is.GreaterThanOrEqualTo(0.3f));
            Assert.That(IntroTimeline.Evaluate(start - Frame).flat, Is.EqualTo(0f));
            Assert.That(IntroTimeline.Evaluate(start + IntroTimeline.FlattenLead / 2f).flat, Is.EqualTo(0.5f).Within(0.01f));
            for (float t = start; t <= IntroTimeline.Lock + Frame; t += Frame)
                Assert.That(IntroTimeline.Evaluate(t).flat - IntroTimeline.Evaluate(t - Frame).flat, Is.LessThan(0.09f), $"t={t}: no snap");
            Assert.That(IntroTimeline.Evaluate(IntroTimeline.Lock - Frame).bloom, Is.LessThan(0.01f), "the bloom has faded by the lock");
        }

        [Test]
        public void ThePulseRampsOverThreeFramesThenWidensAndFades()
        {
            float a1 = IntroTimeline.Evaluate(IntroTimeline.Lock + Frame).pulseAlpha;
            float a2 = IntroTimeline.Evaluate(IntroTimeline.Lock + 2f * Frame).pulseAlpha;
            float a3 = IntroTimeline.Evaluate(IntroTimeline.Lock + 3f * Frame).pulseAlpha;
            Assert.That(IntroTimeline.Evaluate(IntroTimeline.Lock).pulseAlpha, Is.EqualTo(0f));
            Assert.That(a1, Is.GreaterThan(0f).And.LessThan(a2));
            Assert.That(a2, Is.LessThan(a3));
            Assert.That(a3, Is.EqualTo(0.6f).Within(0.01f));
            var late = IntroTimeline.Evaluate(IntroTimeline.Lock + IntroTimeline.PulseAttack + IntroTimeline.PulseDecay * 0.99f);
            Assert.That(late.pulseScale, Is.EqualTo(1.35f).Within(0.01f));
            Assert.That(late.pulseWidth, Is.EqualTo(1f).Within(0.05f));
            Assert.That(IntroTimeline.Evaluate(IntroTimeline.Lock + 0.6f).pulseAlpha, Is.EqualTo(0f));
        }

        [Test]
        public void TheExitFadesTheWordmarkThenOpensTheInnerHexagon()
        {
            Assert.That(IntroTimeline.Evaluate(IntroTimeline.WordmarkFull).wordmark, Is.EqualTo(1f));
            Assert.That(IntroTimeline.Evaluate(IntroTimeline.DissolveStart).wordmark, Is.EqualTo(1f));
            Assert.That(IntroTimeline.Evaluate(IntroTimeline.WordmarkGone).wordmark, Is.EqualTo(0f));
            Assert.That(IntroTimeline.WordmarkGone, Is.LessThanOrEqualTo(IntroTimeline.DissolveStart + 0.25f));
            for (float t = 0f; t <= IntroTimeline.End; t += Frame)
            {
                var p = IntroTimeline.Evaluate(t);
                Assert.That(p.cover, Is.EqualTo(1f), $"t={t}: nothing is a see-through ghost");
                if (p.holeScale > 1.02f) Assert.That(p.wordmark, Is.EqualTo(0f), $"t={t}: the wordmark has gone before the opening grows");
                if (t < IntroTimeline.OpenStart) Assert.That(p.hole, Is.EqualTo(0f), $"t={t}");
            }
            var end = IntroTimeline.Evaluate(IntroTimeline.End);
            Assert.That(end.hole, Is.EqualTo(1f));
            Assert.That(end.holeScale, Is.EqualTo(IntroTimeline.HoleGrowth).Within(1e-3f));
            Assert.That(end.cubeAlpha, Is.EqualTo(0f));
            // the opening's inner hexagon clears the corners of a 21:9 screen by the end
            float inscribed = DemoIntro.InnerRadius * Mathf.Sqrt(3f) / 2f * IntroTimeline.HoleGrowth;
            float halfHeight = DemoIntro.ViewHalfHeight, halfWidth = halfHeight * 21f / 9f;
            Assert.That(inscribed, Is.GreaterThan(new Vector2(halfWidth, halfHeight + Mathf.Abs(DemoIntro.LockupCentreY)).magnitude));
        }

        [Test]
        public void ReducedMotionIsTheStillLockupThenAQuickFade()
        {
            for (float t = 0f; t <= IntroTimeline.ReducedEnd; t += 0.1f)
            {
                var p = IntroTimeline.EvaluateReduced(t);
                Assert.That(Angle(p.frameRotation) + Angle(p.cubeRotation), Is.LessThan(1e-3f), $"t={t}");
                Assert.That(p.flat, Is.EqualTo(1f));
                Assert.That(p.wordmark, Is.EqualTo(1f));
                Assert.That(p.glow + p.packets + p.pulseAlpha + p.bloom + p.hole + p.chamfer, Is.EqualTo(0f), $"t={t}: nothing moves or flashes");
                Assert.That(p.holeScale, Is.EqualTo(1f));
            }
            Assert.That(IntroTimeline.EvaluateReduced(0f).cover, Is.EqualTo(1f));
            Assert.That(IntroTimeline.EvaluateReduced(IntroTimeline.ReducedEnd).cover, Is.EqualTo(0f));
            Assert.That(IntroTimeline.ReducedEnd, Is.LessThan(2f));
        }
    }

    public class DemoIntroModelTests
    {
        const string Model = "Packages/io.devicechain.demo-intro/Runtime/Models/devicechain_mark.glb";

        // symbol.svg's face colours, and the wordmark's (logo.svg)
        static readonly string[] MarkColours = { "#1F425E", "#7AB7D9", "#208CB7", "#52A2C9", "#007BA6", "#006790", "#9ACEEC", "#006B97" };
        static readonly string[] WordmarkColours = { "#FFFFFF", "#208CB7" };
        /// <summary>build_mark.py's chamfer, in units.</summary>
        const float Chamfer = 0.0055f;

        static Mesh MeshOf(string node)
        {
            var go = AssetDatabase.LoadAssetAtPath<GameObject>(Model);
            Assert.That(go, Is.Not.Null, Model);
            var mf = go.GetComponentsInChildren<MeshFilter>(true).FirstOrDefault(m => m.name == node);
            Assert.That(mf, Is.Not.Null, node);
            return mf.sharedMesh;
        }

        static HashSet<string> HexColours(Mesh m) =>
            new HashSet<string>(m.colors.Select(c => "#" + ColorUtility.ToHtmlStringRGB(c.gamma)));

        [Test]
        public void TheMarkUsesOnlyTheBrandColours()
        {
            var used = HexColours(MeshOf("Frame"));
            used.UnionWith(HexColours(MeshOf("Cube")));
            Assert.That(used, Is.SubsetOf(MarkColours));
            // every colour the drawing shows is on the model
            Assert.That(used, Is.EquivalentTo(MarkColours));
            Assert.That(HexColours(MeshOf("Wordmark")), Is.EquivalentTo(WordmarkColours));
        }

        [Test]
        public void TheModelHasTheDrawingsProportions()
        {
            var frame = MeshOf("Frame").bounds;
            Assert.That(frame.extents.y, Is.EqualTo(DemoIntro.OuterRadius).Within(1e-3f), "the hexagon's height");
            Assert.That(frame.extents.x, Is.EqualTo(DemoIntro.OuterRadius * Mathf.Sqrt(3f) / 2f).Within(1e-3f), "the hexagon's width");
            var cube = MeshOf("Cube");
            // the cube's corners are the ones the intro's edge light is drawn along
            var corners = DemoIntro.CubeCorners();
            foreach (var v in cube.vertices)
                Assert.That(corners.Min(c => Vector3.Distance(c, v)), Is.LessThan(1e-3f), $"vertex {v}");
            var word = MeshOf("Wordmark").bounds;
            Assert.That(word.max.y, Is.LessThan(-DemoIntro.OuterRadius), "the wordmark sits below the mark");
            Assert.That(word.min.y, Is.EqualTo(DemoIntro.LockupBottom).Within(0.01f));
            Assert.That(word.extents.x, Is.EqualTo(DemoIntro.LockupHalfWidth).Within(0.01f));
        }

        static Vector3[] Chamfered(Mesh m, float weight)
        {
            var uv2 = new List<Vector2>();
            var uv3 = new List<Vector2>();
            m.GetUVs(1, uv2);
            m.GetUVs(2, uv3);
            Assert.That(uv2.Count, Is.EqualTo(m.vertexCount), "the chamfer's offsets (UV set 1)");
            Assert.That(uv3.Count, Is.EqualTo(m.vertexCount), "the chamfer's offsets (UV set 2)");
            var v = m.vertices;
            return v.Select((p, i) => p + new Vector3(uv2[i].x, uv2[i].y, uv3[i].x) * weight).ToArray();
        }

        static float Area(Vector3[] v, int[] t, int k) =>
            Vector3.Cross(v[t[k + 1]] - v[t[k]], v[t[k + 2]] - v[t[k]]).magnitude * 0.5f;

        [TestCase("Frame")]
        [TestCase("Cube")]
        public void TheChamferHasNoAreaInTheDrawingsShapeAndOpensWhileMoving(string node)
        {
            var m = MeshOf(node);
            var t = m.triangles;
            var flat = Chamfered(m, 0f);
            var full = Chamfered(m, 1f);
            Assert.That(flat, Is.EqualTo(m.vertices), "weight 0 is the drawing");
            int strips = 0;
            float hidden = 0f, opened = 0f;
            for (int k = 0; k < t.Length; k += 3)
            {
                float a0 = Area(flat, t, k), a1 = Area(full, t, k);
                if (a0 < 1e-5f)
                {
                    strips++;
                    hidden += a0;
                    opened += a1;
                    Assert.That(a1, Is.GreaterThan(1e-7f), $"triangle {k / 3} opens");
                }
            }
            Assert.That(strips, Is.GreaterThan(t.Length / 6), "most of the chamfer's triangles");
            Assert.That(hidden, Is.LessThan(1e-4f), "in the drawing's shape the chamfer covers (almost) nothing");
            Assert.That(opened, Is.GreaterThan(hidden * 10f + 1e-4f), "moving, it has a visible width");
            var offsets = full.Select((p, i) => (p - flat[i]).magnitude).Max();
            Assert.That(offsets, Is.GreaterThan(Chamfer * 0.9f).And.LessThan(Chamfer * 3f));
        }
    }

    public class DemoIntroComponentTests
    {
        GameObject go;
        bool disabledBefore, reducedBefore;

        [SetUp]
        public void SetUp()
        {
            disabledBefore = DemoIntro.Disabled;
            reducedBefore = DemoIntro.PreferReducedMotion;
            DemoIntro.Disabled = false;
            DemoIntro.PreferReducedMotion = false;
            var prefab = Resources.Load<GameObject>(DemoIntro.ResourceName);
            Assert.That(prefab, Is.Not.Null, "the packaged prefab");
            go = Object.Instantiate(prefab);
        }

        [TearDown]
        public void TearDown()
        {
            if (go != null)
            {
                go.GetComponent<DemoIntro>().Teardown();
                Object.DestroyImmediate(go);
            }
            DemoIntro.Disabled = disabledBefore;
            DemoIntro.PreferReducedMotion = reducedBefore;
        }

        static Transform Node(DemoIntro intro, string name) =>
            intro.IntroCamera.transform.parent.GetComponentsInChildren<Transform>(true).First(t => t.name == name);

        [Test]
        public void EvaluatePosesTheRigAndTeardownRemovesIt()
        {
            var intro = go.GetComponent<DemoIntro>();
            intro.Evaluate(0.6f);
            Assert.That(intro.IntroCamera, Is.Not.Null);
            Assert.That(intro.IntroCamera.orthographic, Is.True);
            var frame = Node(intro, "Frame");
            Assert.That(Quaternion.Angle(frame.localRotation, Quaternion.identity), Is.GreaterThan(1f));
            intro.Evaluate(IntroTimeline.Settled);
            Assert.That(Quaternion.Angle(frame.localRotation, Quaternion.identity), Is.LessThan(1e-3f));
            intro.Teardown();
            Assert.That(intro.IntroCamera, Is.Null);
            Assert.That(frame == null, Is.True, "the rig is destroyed");
        }

        [Test]
        public void TheWordmarkIsNeverScaledAndFinishesItsRise()
        {
            var intro = go.GetComponent<DemoIntro>();
            intro.Evaluate(IntroTimeline.WordmarkIn + 0.05f);
            var word = Node(intro, "Wordmark");
            float rising = word.localPosition.y;
            intro.Evaluate(IntroTimeline.WordmarkFull);
            float rest = word.localPosition.y;
            Assert.That(rest - rising, Is.GreaterThan(0f), "it rises into place");
            for (float t = IntroTimeline.WordmarkFull; t < IntroTimeline.WordmarkGone; t += 1f / 60f)
            {
                intro.Evaluate(t);
                Assert.That(word.lossyScale.x, Is.EqualTo(1f).Within(1e-5f), $"t={t}");
                Assert.That(word.localPosition.y, Is.EqualTo(rest).Within(1e-6f), $"t={t}");
            }
        }

        static Color32[] Render(DemoIntro intro, float t, int w, int h)
        {
            intro.Evaluate(t, false, (float)w / h);
            var rt = new RenderTexture(w, h, 0, RenderTextureFormat.ARGB32, RenderTextureReadWrite.sRGB);
            try
            {
                intro.RenderInto(rt);
                var prev = RenderTexture.active;
                RenderTexture.active = rt;
                var tex = new Texture2D(w, h, TextureFormat.RGBA32, false);
                tex.ReadPixels(new Rect(0, 0, w, h), 0, 0);
                RenderTexture.active = prev;
                var px = tex.GetPixels32();
                Object.DestroyImmediate(tex);
                return px;
            }
            finally
            {
                rt.Release();
                Object.DestroyImmediate(rt);
            }
        }

        [Test]
        public void TheImageCoversDuringTheHoldAndOpensOntoTheDemoAtTheEnd()
        {
            var intro = go.GetComponent<DemoIntro>();
            var hold = Render(intro, IntroTimeline.DissolveStart, 160, 90);
            Assert.That(hold.All(p => p.a == 255), Is.True, "the hold covers the whole screen");
            var opening = Render(intro, IntroTimeline.OpenStart + 0.2f, 160, 90);
            int clear = opening.Count(p => p.a == 0);
            Assert.That(clear, Is.GreaterThan(0).And.LessThan(opening.Length), "part open");
            // the opening is centred on the mark: the centre of the mark's hexagon is clear
            var end = Render(intro, IntroTimeline.End - 1e-3f, 160, 90);
            Assert.That(end.Count(p => p.a == 0), Is.GreaterThan(end.Length * 0.99f), "open onto the demo");
            Assert.That(end.Where(p => p.a == 0).All(p => p.r == 0 && p.g == 0 && p.b == 0), Is.True,
                "where it is open nothing of the intro remains (premultiplied)");
        }

        [Test]
        public void ASkipFadesOutFromWhereverTheIntroIs()
        {
            var intro = go.GetComponent<DemoIntro>();
            intro.destroyOnComplete = false;
            intro.Play();
            Assert.That(intro.IsPlaying, Is.True);
            Assert.That(intro.IsDone(1f), Is.False);
            intro.Skip();                              // at time 0
            Assert.That(intro.PoseAt(IntroTimeline.SkipFade * 0.5f).cover, Is.EqualTo(0.5f).Within(1e-4f));
            Assert.That(intro.IsDone(IntroTimeline.SkipFade), Is.True);
        }

        [Test]
        public void ADisabledIntroCompletesAtOnce()
        {
            var intro = go.GetComponent<DemoIntro>();
            intro.destroyOnComplete = false;
            int calls = 0;
            intro.Completed += () => calls++;
            DemoIntro.Disabled = true;
            intro.Play();
            Assert.That(calls, Is.EqualTo(1));
            Assert.That(intro.IsComplete, Is.True);
            Assert.That(intro.IntroCamera, Is.Null, "nothing was built");
        }

        [Test]
        public void ReducedMotionIsChosenByTheComponentOrTheGlobalPreference()
        {
            var intro = go.GetComponent<DemoIntro>();
            intro.destroyOnComplete = false;
            DemoIntro.PreferReducedMotion = true;
            intro.Play();
            Assert.That(intro.ReducedMotionActive, Is.True);
            Assert.That(intro.IsDone(IntroTimeline.ReducedEnd), Is.True);
            Assert.That(intro.IsDone(IntroTimeline.ReducedEnd - 0.01f), Is.False);
        }
    }
}
