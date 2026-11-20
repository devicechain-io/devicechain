// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using DeviceChain.Harborpulse.Model;
using NUnit.Framework;

namespace DeviceChain.Harborpulse.Tests
{
    /// <summary>U3 lighting: soft overcast days, a readable night and a storm that darkens the sky rather than the town.</summary>
    public class U3LightingTests
    {
        static double Luma(Rgb c) => 0.2126 * c.R + 0.7152 * c.G + 0.0722 * c.B;

        [Test]
        public void TheDayIsSoftOvercast()
        {
            var noon = DayLighting.Evaluate(12.25, new Weather(0, 0));
            Assert.That(noon.SunIntensity, Is.GreaterThan(1.5).And.LessThan(1.8), "the high cloud softens the sun");
            Assert.That(noon.ShadowStrength, Is.LessThan(0.6), "and its shadows");
            Assert.That(Luma(noon.AmbientSky), Is.GreaterThan(0.5), "a cloudy day's light is mostly sky");
            Assert.That(noon.Zenith.B - noon.Zenith.R, Is.LessThan(0.45), "a pale grey-blue sky, not a saturated one");
            Assert.That(noon.FogDensity, Is.InRange(0.0001, 0.0002), "light haze leaves the middle hillside readable");
            Assert.That(noon.CloudCover, Is.EqualTo(DayLighting.Cloud), "the sky draws the high cloud the light is softened by");
            Assert.That(noon.Overcast, Is.EqualTo(0), "the storm's own cover stays the storm's");
            Assert.That(DayLighting.Evaluate(16.5, new Weather(1, 1)).CloudCover, Is.EqualTo(1), "the storm's cover is total");
        }

        [Test]
        public void TheNightKeepsAMoonlitFloor()
        {
            foreach (var hour in new[] { 0.0, 2.5, 21.5, 23.0 })
                foreach (var storm in new[] { 0.0, 1.0 })
                {
                    var s = DayLighting.Evaluate(hour, new Weather(storm, storm));
                    Assert.That(s.AmbientSky.B, Is.GreaterThanOrEqualTo(0.24 - 1e-9), "sky ambient at " + hour + " storm " + storm);
                    Assert.That(s.AmbientEquator.G, Is.GreaterThanOrEqualTo(0.125 - 1e-9), "equator ambient at " + hour + " storm " + storm);
                }
        }

        [Test]
        public void TheStormDarkensTheSkyNotTheTown()
        {
            var clear = DayLighting.Evaluate(17.5, new Weather(0, 0));
            var storm = DayLighting.Evaluate(17.5, new Weather(1, 1));
            Assert.That(storm.SunIntensity, Is.LessThan(clear.SunIntensity * 0.5));
            Assert.That(Luma(storm.AmbientSky), Is.GreaterThan(0.75 * Luma(clear.AmbientSky)), "the town stays legible under the storm");
            Assert.That(storm.ShadowStrength, Is.LessThan(clear.ShadowStrength));
        }
    }
}
