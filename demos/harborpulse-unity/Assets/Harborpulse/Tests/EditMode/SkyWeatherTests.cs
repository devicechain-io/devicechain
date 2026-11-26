// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using DeviceChain.Harborpulse.Model;
using NUnit.Framework;

namespace DeviceChain.Harborpulse.Tests
{
    /// <summary>The sky as a function of the scenario hour and the storm beat: what the U5 pass made visible must stay visible.</summary>
    public class SkyWeatherTests
    {
        const double From = 16, To = 22.5;
        static SkyState At(double hour) => DayLighting.Evaluate(hour, Weather.Window(From, To, hour));
        static double Luma(Rgb c) => 0.2126 * c.R + 0.7152 * c.G + 0.0722 * c.B;
        static double Distance(Rgb a, Rgb b) => Math.Sqrt((a.R - b.R) * (a.R - b.R) + (a.G - b.G) * (a.G - b.G) + (a.B - b.B) * (a.B - b.B));

        static double[] Direction(double elevationDeg, double azimuthDeg)
        {
            var e = elevationDeg * Math.PI / 180; var a = azimuthDeg * Math.PI / 180;
            return new[] { Math.Sin(a) * Math.Cos(e), Math.Sin(e), Math.Cos(a) * Math.Cos(e) };
        }

        static double AngleDeg(double[] a, double[] b) =>
            Math.Acos(Math.Max(-1, Math.Min(1, a[0] * b[0] + a[1] * b[1] + a[2] * b[2]))) * 180 / Math.PI;

        [Test]
        public void TheCloudBuildsBeforeTheStormClosesInItAndClearsAfter()
        {
            Assert.That(At(12.5).CloudCover, Is.InRange(0.3, 0.5), "fair-weather cloud at noon, with sky between");
            Assert.That(At(From - 1).CloudCover, Is.GreaterThanOrEqualTo(0.75), "an hour ahead the front has nearly closed the sky");
            Assert.That(At(From - 1).Overcast, Is.EqualTo(0), "but the storm itself has not begun");
            Assert.That(At(18).CloudCover, Is.GreaterThanOrEqualTo(0.95), "the deck is closed in the storm");
            Assert.That(At(To + 2).CloudCover, Is.LessThanOrEqualTo(0.4), "two hours after, the sky has cleared");
            Assert.That(At(To + 0.75).CloudCover, Is.InRange(0.3, 0.9), "and in between it is breaking up, not switched off");
            Assert.That(At(From - 1).CloudDepth, Is.GreaterThan(At(12.5).CloudDepth), "the bases of the building cloud darken");
            Assert.That(At(18).CloudDepth, Is.EqualTo(1));
        }

        [Test]
        public void TheSkyNeverJumpsAndTheDayJoinsUpAtMidnight()
        {
            SkyState previous = null;
            for (var minute = 0; minute <= 24 * 60; minute++)
            {
                var s = At(minute / 60.0);
                if (previous != null)
                {
                    Assert.That(Math.Abs(s.CloudCover - previous.CloudCover), Is.LessThan(0.02), "cover at minute " + minute);
                    Assert.That(Distance(s.ClearZenith, previous.ClearZenith), Is.LessThan(0.03), "zenith at minute " + minute);
                    Assert.That(Distance(s.CloudLit, previous.CloudLit), Is.LessThan(0.04), "cloud at minute " + minute);
                    Assert.That(Math.Abs(s.CloudDriftX - previous.CloudDriftX) + Math.Abs(s.CloudDriftZ - previous.CloudDriftZ), Is.LessThan(0.05), "drift at minute " + minute);
                }

                previous = s;
            }

            var before = At(23.9999); var after = At(0.0001);
            Assert.That(after.CloudCover, Is.EqualTo(before.CloudCover).Within(1e-3));
            Assert.That(after.CloudDriftX, Is.EqualTo(before.CloudDriftX).Within(1e-3));
            Assert.That(after.CloudDriftZ, Is.EqualTo(before.CloudDriftZ).Within(1e-3));
        }

        [Test]
        public void TheFrontIsTheSameWhicheverSideOfMidnightTheHourIsGiven()
        {
            // the live source hands the storm window an hour that may be 24 ahead; the clearing of a late beat runs past midnight
            foreach (var hour in new[] { 13.0, 15.0, 23.0 })
                Assert.That(Weather.Window(From, To, hour + 24).Front, Is.EqualTo(Weather.Window(From, To, hour).Front).Within(1e-9), "hour " + hour);
            Assert.That(Weather.Window(20, 23.5, 0.5).Front, Is.InRange(0.05, 0.95), "a beat ending at 23:30 is still clearing at 00:30");
            Assert.That(Weather.Window(From, To, 12).Rain, Is.EqualTo(0));
            Assert.That(At(15.5).Rain, Is.EqualTo(0), "the cloud builds dry: it does not rain before the storm");
            Assert.That(At(To + 1).Rain, Is.EqualTo(0));
            Assert.That(At(18).Rain, Is.GreaterThan(0.9));
        }

        [Test]
        public void TheJudgedHoursEachHaveTheirOwnSky()
        {
            // the hours the look is reviewed at: dawn, noon, cloud building, storm, storm dusk, clearing night, clear night
            var hours = new[] { 6.4, 12.5, 15.4, 17.2, 18.3, 23.6, 2.0 };
            for (var i = 0; i < hours.Length; i++)
                for (var j = i + 1; j < hours.Length; j++)
                {
                    var a = At(hours[i]); var b = At(hours[j]);
                    var difference = Distance(a.ClearZenith, b.ClearZenith) + Distance(a.ClearHorizon, b.ClearHorizon) + Distance(a.CloudLit, b.CloudLit)
                        + Math.Abs(a.CloudCover - b.CloudCover) + Math.Abs(a.Stars - b.Stars) * 0.5;
                    Assert.That(difference, Is.GreaterThan(0.2), hours[i] + " against " + hours[j]);
                }

            var dawn = At(6.4);
            Assert.That(dawn.TwilightStrength, Is.GreaterThan(0.5), "a dawn glows");
            Assert.That(dawn.ClearHorizon.R, Is.GreaterThan(dawn.ClearHorizon.B), "rose at the horizon");
            Assert.That(dawn.ClearZenith.B, Is.GreaterThan(dawn.ClearZenith.R * 2), "under a blue-violet zenith");
            Assert.That(At(12.5).TwilightStrength, Is.EqualTo(0));
            var clear = At(2.0);
            Assert.That(clear.ClearZenith.B, Is.GreaterThan(0.05), "the night sky is a deep blue, not black");
            Assert.That(clear.ClearHorizon.B, Is.GreaterThan(clear.ClearZenith.B), "paler at the horizon");
            var storm = At(17.2);
            Assert.That(Luma(storm.CloudShade), Is.LessThan(0.6 * Luma(At(12.5).CloudShade)), "the bases of the storm cloud are dark");
            Assert.That(Luma(storm.ClearHorizon), Is.GreaterThan(Luma(storm.CloudShade)), "with a lighter band at the horizon so the land separates");
        }

        [Test]
        public void NoonKeepsTheExposureTheTownWasGradedFor()
        {
            // the U4 values at 12:30 under its fixed 55 % cloud; the varying cover may move them by 15 %, no more
            const double sun = 1.638, sky = 0.608, equator = 0.681;
            var noon = At(12.5);
            Assert.That(noon.SunIntensity, Is.EqualTo(sun).Within(sun * 0.15));
            Assert.That(Luma(noon.AmbientSky), Is.EqualTo(sky).Within(sky * 0.15));
            Assert.That(Luma(noon.AmbientEquator), Is.EqualTo(equator).Within(equator * 0.15));
            Assert.That(At(9.5).ShadowStrength, Is.GreaterThan(At(15.5).ShadowStrength), "a clearer morning is crisper than the build-up");
            Assert.That(At(15.5).SunIntensity, Is.LessThan(At(9.5).SunIntensity * 0.95), "and the build-up dims the sun");
        }

        [Test]
        public void TheTownIsNeverLeftWithoutLight()
        {
            // a storm dusk and the hour before dawn have neither sun nor moon: the ambient floor is lifted instead of leaving a hole
            for (var minute = 0; minute < 24 * 60; minute += 5)
            {
                var s = At(minute / 60.0);
                var key = s.SunIntensity + s.MoonIntensity;
                Assert.That(s.AmbientSky.B, Is.GreaterThanOrEqualTo(0.24 - 1e-9), "the night floor at minute " + minute);
                if (key < 0.05)
                    Assert.That(Luma(s.AmbientSky), Is.GreaterThan(0.35), "a keyless scene is carried by the ambient, minute " + minute);
            }

            Assert.That(At(18.3).SunIntensity + At(18.3).MoonIntensity, Is.LessThan(0.05), "the storm dusk is the keyless case this pins");
            // and the grade opens up for dim daylight, without touching the noon or the night it was tuned for
            Assert.That(At(12.5).ExposureLift, Is.EqualTo(0).Within(0.01));
            Assert.That(At(1.0).ExposureLift, Is.EqualTo(0.55 + DayLighting.NightStoryLift).Within(0.01), "the eye opening, and the story lift a recorded cut needs");
            Assert.That(At(6.4).ExposureLift, Is.GreaterThan(0.6), "dawn");
            Assert.That(At(18.3).ExposureLift, Is.GreaterThan(0.6), "storm dusk");
        }

        [Test]
        public void TheSunAndTheMoonMoveWithoutAJump()
        {
            double[] sun = null, moon = null;
            for (var minute = 0; minute <= 24 * 60; minute++)
            {
                var s = At(minute / 60.0);
                var a = Direction(s.SunElevationDeg, s.SunAzimuthDeg); var b = Direction(s.MoonElevationDeg, s.MoonAzimuthDeg);
                if (sun != null)
                {
                    Assert.That(AngleDeg(a, sun), Is.LessThan(2), "sun at minute " + minute);
                    Assert.That(AngleDeg(b, moon), Is.LessThan(2), "moon at minute " + minute);
                }

                sun = a; moon = b;
            }

            Assert.That(At(0.25).MoonElevationDeg, Is.EqualTo(DayLighting.MoonMaxElevationDeg).Within(0.5), "the moon culminates half way through its night");
            Assert.That(At(12).MoonElevationDeg, Is.LessThan(-20), "and is well under the horizon at noon");
        }
    }
}
