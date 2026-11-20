// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using DeviceChain.Harborpulse.Model;
using NUnit.Framework;

namespace DeviceChain.Harborpulse.Tests
{
    public sealed class AlarmLabelPaletteTests
    {
        [Test]
        public void WeatherIndependentAlarmLabelExceedsAccessibleContrast()
        {
            // WCAG sRGB transfer function, independent of Unity lighting. Both night and storm use this same opaque palette.
            double Linear(double c) => c <= .04045 ? c / 12.92 : Math.Pow((c + .055) / 1.055, 2.4);
            double Luma(double r, double g, double b) => .2126 * Linear(r) + .7152 * Linear(g) + .0722 * Linear(b);
            var foreground = Luma(AlarmLabelPalette.ForegroundR, AlarmLabelPalette.ForegroundG, AlarmLabelPalette.ForegroundB);
            var background = Luma(AlarmLabelPalette.BackgroundR, AlarmLabelPalette.BackgroundG, AlarmLabelPalette.BackgroundB);
            Assert.That((foreground + .05) / (background + .05), Is.GreaterThanOrEqualTo(4.5));
        }
    }
}
