// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using DeviceChain.Sitepulse.DevicePlane;
using DeviceChain.Sitepulse.Simulation;

namespace DeviceChain.Sitepulse.App
{
    /// <summary>Hands samples to whoever is listening now. The plane's links are built before the recorder exists; this is what they were given.</summary>
    sealed class SampleRelay : IAckedSampleSink
    {
        volatile IAckedSampleSink target;

        public IAckedSampleSink Target
        {
            set => target = value;
        }

        public void Record(string externalId, string deviceToken, Sample sample) => target?.Record(externalId, deviceToken, sample);
    }
}
