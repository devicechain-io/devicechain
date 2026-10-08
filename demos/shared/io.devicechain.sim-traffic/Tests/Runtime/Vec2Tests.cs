// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using DeviceChain.Sim.Traffic.Geometry;
using NUnit.Framework;

namespace DeviceChain.Sim.Traffic.Tests
{
    // These tests are compiled by BOTH the Unity Test Runner (its NUnit 3.5 fork) and dotnet (NUnit 3.14, and a
    // netstandard2.1 compile against the oldest NUnit 3.x). Keep to the shared subset: [Test], [TestCase],
    // Assert.That with Is./Has. constraints, Assert.Ignore, [Category]. No async tests, no [Timeout].
    public class Vec2Tests
    {
        const double Eps = 1e-12;

        static void AssertVec(Vec2 actual, double x, double z, double eps = Eps)
        {
            Assert.That(actual.X, Is.EqualTo(x).Within(eps), "X of " + actual);
            Assert.That(actual.Z, Is.EqualTo(z).Within(eps), "Z of " + actual);
        }

        [Test]
        public void ConstructorKeepsComponents()
        {
            var v = new Vec2(3.5, -2.25);
            Assert.That(v.X, Is.EqualTo(3.5));
            Assert.That(v.Z, Is.EqualTo(-2.25));
        }

        [Test]
        public void DefaultIsZero()
        {
            AssertVec(default(Vec2), 0, 0, 0);
            AssertVec(Vec2.Zero, 0, 0, 0);
        }

        [Test]
        public void AddSubtractNegate()
        {
            var a = new Vec2(1, 2);
            var b = new Vec2(-4, 0.5);
            AssertVec(a + b, -3, 2.5);
            AssertVec(a - b, 5, 1.5);
            AssertVec(b - a, -5, -1.5);
            AssertVec(-a, -1, -2);
        }

        [Test]
        public void ScaleIsCommutative()
        {
            var a = new Vec2(1.5, -3);
            AssertVec(a * 2, 3, -6);
            AssertVec(2 * a, 3, -6);
            AssertVec(a * 0, 0, 0);
        }

        [Test]
        public void DotProduct()
        {
            Assert.That(Vec2.Dot(new Vec2(1, 2), new Vec2(3, 4)), Is.EqualTo(11));
            Assert.That(Vec2.Dot(new Vec2(1, 0), new Vec2(0, 1)), Is.EqualTo(0));
        }

        // Cross is X1*Z2 - Z1*X2: positive when b lies counter-clockwise of a seen from above (+X east, +Z north).
        [Test]
        public void CrossProductSignAndAntisymmetry()
        {
            var east = new Vec2(1, 0);
            var north = new Vec2(0, 1);
            Assert.That(Vec2.Cross(east, north), Is.EqualTo(1));
            Assert.That(Vec2.Cross(north, east), Is.EqualTo(-1));
            Assert.That(Vec2.Cross(new Vec2(2, 3), new Vec2(4, 6)), Is.EqualTo(0), "parallel vectors");
            Assert.That(Vec2.Cross(new Vec2(1, 2), new Vec2(3, 4)), Is.EqualTo(-2));
        }

        [Test]
        public void Length()
        {
            Assert.That(new Vec2(3, 4).Length, Is.EqualTo(5));
            Assert.That(new Vec2(-3, -4).Length, Is.EqualTo(5));
            Assert.That(Vec2.Zero.Length, Is.EqualTo(0));
        }

        // Headings across the simulations are degrees CLOCKWISE from north (+Z), as seen from above, so a positive
        // rotation turns north towards east. This is the convention the quarry footprints already use (forward =
        // (sin h, cos h)), and the opposite of the mathematical counter-clockwise default.
        [TestCase(0, 0.0, 1.0)]
        [TestCase(90, 1.0, 0.0)]
        [TestCase(180, 0.0, -1.0)]
        [TestCase(270, -1.0, 0.0)]
        [TestCase(-90, -1.0, 0.0)]
        [TestCase(45, 0.70710678118654757, 0.70710678118654757)]
        public void RotateNorthIsClockwiseHeading(double degrees, double x, double z)
        {
            AssertVec(new Vec2(0, 1).Rotate(degrees), x, z, 1e-12);
        }

        [Test]
        public void RotateEastByNinetyPointsSouth()
        {
            AssertVec(new Vec2(1, 0).Rotate(90), 0, -1);
        }

        [Test]
        public void RotateKeepsLengthAndComposes()
        {
            var v = new Vec2(2.5, -1.25);
            Assert.That(v.Rotate(37).Length, Is.EqualTo(v.Length).Within(1e-12));
            AssertVec(v.Rotate(30).Rotate(50), v.Rotate(80).X, v.Rotate(80).Z, 1e-12);
            AssertVec(v.Rotate(360), v.X, v.Z, 1e-12);
            AssertVec(v.Rotate(25).Rotate(-25), v.X, v.Z, 1e-12);
        }

        [Test]
        public void RotateAgreesWithCross()
        {
            // a clockwise turn puts the result on the clockwise side: Cross(v, v rotated clockwise) < 0
            var v = new Vec2(0.3, 0.9);
            Assert.That(Vec2.Cross(v, v.Rotate(10)), Is.LessThan(0));
            Assert.That(Vec2.Cross(v, v.Rotate(-10)), Is.GreaterThan(0));
        }

        [Test]
        public void ToStringIsInvariantXThenZ()
        {
            Assert.That(new Vec2(1.5, -2).ToString(), Is.EqualTo("(1.5, -2)"));
            Assert.That(new Vec2(0.1, 1e21).ToString(), Is.EqualTo("(0.1, 1E+21)"));
        }

        [Test]
        public void EqualityIsComponentWise()
        {
            var a = new Vec2(1.5, -2);
            Assert.That(a.Equals(new Vec2(1.5, -2)), Is.True);
            Assert.That(a.Equals((object)new Vec2(1.5, -2)), Is.True);
            Assert.That(a.Equals(new Vec2(-2, 1.5)), Is.False, "X and Z swapped");
            Assert.That(a.Equals(new Vec2(1.5, 2)), Is.False, "Z differs");
            Assert.That(a.Equals(new Vec2(-1.5, -2)), Is.False, "X differs");
            Assert.That(a.Equals((object)null), Is.False);
            Assert.That(a.Equals((object)1.5), Is.False);
            Assert.That(new Vec2(1.5, -2).GetHashCode(), Is.EqualTo(a.GetHashCode()));
        }

        [Test]
        public void HashUsesBothComponents()
        {
            // Not a contract (unequal values may collide), but a hash that ignored a component or its order would
            // send every vehicle in one lane to one bucket.
            var a = new Vec2(1, 2);
            Assert.That(new Vec2(1, 3).GetHashCode(), Is.Not.EqualTo(a.GetHashCode()), "Z ignored");
            Assert.That(new Vec2(3, 2).GetHashCode(), Is.Not.EqualTo(a.GetHashCode()), "X ignored");
            Assert.That(new Vec2(2, 1).GetHashCode(), Is.Not.EqualTo(a.GetHashCode()), "components unordered");
        }

        [Test]
        public void EqualVectorsHashAlikeAtSignedZeroAndNaN()
        {
            var zero = new Vec2(0.0, 0.0);
            var negZero = new Vec2(-0.0, -0.0);
            Assert.That(zero.Equals(negZero), Is.True);
            Assert.That(negZero.GetHashCode(), Is.EqualTo(zero.GetHashCode()));

            // two NaNs with different payloads
            var nanA = new Vec2(double.NaN, 1);
            var nanB = new Vec2(System.BitConverter.Int64BitsToDouble(unchecked((long)0xFFF8000000000001UL)), 1);
            Assert.That(double.IsNaN(nanB.X), Is.True);
            Assert.That(nanA.Equals(nanB), Is.True);
            Assert.That(nanB.GetHashCode(), Is.EqualTo(nanA.GetHashCode()));
        }
    }
}
