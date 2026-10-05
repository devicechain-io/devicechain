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
        static float Angle(Quaternion q) => Quaternion.Angle(q, Quaternion.identity);

        [Test]
        public void BeatsRunInOrderAndLastAboutThreeAndAHalfSeconds()
        {
            Assert.That(IntroTimeline.TraceStart, Is.LessThan(IntroTimeline.TraceEnd));
            Assert.That(IntroTimeline.TraceEnd, Is.LessThan(IntroTimeline.Lock));
            Assert.That(IntroTimeline.SweepEnd, Is.LessThan(IntroTimeline.Lock));
            Assert.That(IntroTimeline.Lock, Is.LessThan(IntroTimeline.WordmarkIn));
            Assert.That(IntroTimeline.WordmarkFull, Is.LessThan(IntroTimeline.DissolveStart));
            Assert.That(IntroTimeline.DissolveStart - IntroTimeline.WordmarkFull, Is.EqualTo(0.5f).Within(1e-4f), "the wordmark holds for half a second");
            Assert.That(IntroTimeline.End, Is.InRange(3.3f, 3.8f));
        }

        [Test]
        public void LocksExactlyIntoTheIsometricPoseWithFlatBrandColours()
        {
            var p = IntroTimeline.Evaluate(IntroTimeline.Lock);
            Assert.That(Angle(p.frameRotation), Is.LessThan(1e-3f));
            Assert.That(Angle(p.cubeRotation), Is.LessThan(1e-3f));
            Assert.That(p.cubeScale, Is.EqualTo(1f));
            Assert.That(p.flat, Is.EqualTo(1f));
            // and it stays locked through the hold
            foreach (float t in new[] { IntroTimeline.WordmarkFull, IntroTimeline.DissolveStart, IntroTimeline.End })
            {
                var q = IntroTimeline.Evaluate(t);
                Assert.That(Angle(q.frameRotation) + Angle(q.cubeRotation), Is.LessThan(1e-3f), $"t={t}");
                Assert.That(q.flat, Is.EqualTo(1f), $"t={t}");
                Assert.That(q.glow, Is.EqualTo(0f), $"t={t}: no glow on the locked mark");
                if (t >= IntroTimeline.DissolveStart) Assert.That(q.particles, Is.EqualTo(0f), $"t={t}: the particles have gone");
            }
        }

        [Test]
        public void IsInMotionAndLitBeforeTheLock()
        {
            var p = IntroTimeline.Evaluate(IntroTimeline.Lock - 0.3f);
            Assert.That(Angle(p.frameRotation), Is.GreaterThan(1f));
            Assert.That(Angle(p.cubeRotation), Is.GreaterThan(1f));
            Assert.That(p.flat, Is.EqualTo(0f));
            Assert.That(p.cubeScale, Is.LessThan(1f));
            // still moving when it locks, so the lock reads as a catch: the easing's last
            // stretch covers ground at a speed well above zero (Quaternion.Angle cannot see the
            // last frame's few hundredths of a degree, so the easing is measured directly)
            float speedAtLock = (IntroTimeline.Settle(1f) - IntroTimeline.Settle(1f - 1e-3f)) / 1e-3f;
            Assert.That(speedAtLock, Is.GreaterThan(0.05f));
            Assert.That(IntroTimeline.Settle(1f), Is.EqualTo(1f).Within(1e-6f));
            Assert.That(IntroTimeline.Settle(0f), Is.EqualTo(0f));
        }

        [Test]
        public void StartsFromBlackAndCoversUntilTheDissolve()
        {
            var start = IntroTimeline.Evaluate(0f);
            Assert.That(start.light, Is.EqualTo(0f));
            Assert.That(start.trace, Is.EqualTo(0f));
            Assert.That(start.wordmark, Is.EqualTo(0f));
            for (float t = 0f; t <= IntroTimeline.DissolveStart; t += 0.05f)
                Assert.That(IntroTimeline.Evaluate(t).cover, Is.EqualTo(1f), $"t={t}");
            Assert.That(IntroTimeline.Evaluate(IntroTimeline.End).cover, Is.EqualTo(0f));
            Assert.That(IntroTimeline.Evaluate(IntroTimeline.WordmarkFull).wordmark, Is.EqualTo(1f));
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
                Assert.That(p.glow + p.particles + p.pulse * (1f - p.pulse) + p.push, Is.EqualTo(0f), $"t={t}: nothing moves");
            }
            Assert.That(IntroTimeline.EvaluateReduced(0f).cover, Is.EqualTo(1f));
            Assert.That(IntroTimeline.EvaluateReduced(IntroTimeline.ReducedEnd).cover, Is.EqualTo(0f));
            Assert.That(IntroTimeline.ReducedEnd, Is.LessThan(2f));
        }
    }

    public class DemoIntroModelTests
    {
        const string Model = "Packages/io.devicechain.demo-intro/Runtime/Models/devicechain_mark.glb";
        const string Prefab = "Packages/io.devicechain.demo-intro/Runtime/Resources/" + DemoIntro.ResourceName + ".prefab";

        // symbol.svg's face colours, and the wordmark's (logo.svg)
        static readonly string[] MarkColours = { "#1F425E", "#7AB7D9", "#208CB7", "#52A2C9", "#007BA6", "#006790", "#9ACEEC", "#006B97" };
        static readonly string[] WordmarkColours = { "#FFFFFF", "#208CB7" };

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
            // the cube's corners are the ones the intro's edge glow is drawn along
            var corners = DemoIntro.CubeCorners();
            foreach (var v in cube.vertices)
                Assert.That(corners.Min(c => Vector3.Distance(c, v)), Is.LessThan(1e-3f), $"vertex {v}");
            var word = MeshOf("Wordmark").bounds;
            Assert.That(word.max.y, Is.LessThan(-DemoIntro.OuterRadius), "the wordmark sits below the mark");
            Assert.That(word.min.y, Is.EqualTo(DemoIntro.LockupBottom).Within(0.01f));
            Assert.That(word.extents.x, Is.EqualTo(DemoIntro.LockupHalfWidth).Within(0.01f));
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

        [Test]
        public void EvaluatePosesTheRigAndTeardownRemovesIt()
        {
            var intro = go.GetComponent<DemoIntro>();
            intro.Evaluate(IntroTimeline.Lock - 0.5f);
            Assert.That(intro.IntroCamera, Is.Not.Null);
            Assert.That(intro.IntroCamera.orthographic, Is.True);
            var frame = intro.IntroCamera.transform.parent.GetComponentsInChildren<Transform>(true).First(t => t.name == "Frame");
            Assert.That(Quaternion.Angle(frame.localRotation, Quaternion.identity), Is.GreaterThan(1f));
            intro.Evaluate(IntroTimeline.Lock);
            Assert.That(Quaternion.Angle(frame.localRotation, Quaternion.identity), Is.LessThan(1e-3f));
            intro.Teardown();
            Assert.That(intro.IntroCamera, Is.Null);
            Assert.That(frame == null, Is.True, "the rig is destroyed");
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
