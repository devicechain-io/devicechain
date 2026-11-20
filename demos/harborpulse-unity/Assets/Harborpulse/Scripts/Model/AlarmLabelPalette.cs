// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

namespace DeviceChain.Harborpulse.Model
{
    /// <summary>Opaque sRGB alarm-label palette. Identical at night and in storm: neither weather nor severity fades evidence text.</summary>
    public static class AlarmLabelPalette
    {
        public const float ForegroundR = .98f, ForegroundG = .91f, ForegroundB = .69f;
        public const float BackgroundR = .012f, BackgroundG = .020f, BackgroundB = .030f;
    }
}
