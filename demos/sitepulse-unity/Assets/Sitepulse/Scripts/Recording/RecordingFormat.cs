// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;

namespace DeviceChain.Sitepulse.Recording
{
    /// <summary>The names of the files in a run's directory.</summary>
    public static class RecordingFiles
    {
        public const string RunJson = "run.json";
        public const string SimBin = "sim.bin";
        public const string Observed = "observed.ndjson";
        public const string Device = "device.ndjson";
        public const string Presenter = "presenter.ndjson";
    }

    public static class RecordingFormat
    {
        /// <summary>The version of the whole recording (run.json's <c>formatVersion</c>) and of sim.bin's header.</summary>
        public const int Version = 1;
    }

    /// <summary>A recording that cannot be trusted as it is: wrong version, cut short, malformed. Never swallowed into a default.</summary>
    public sealed class RecordingFormatException : Exception
    {
        public RecordingFormatException(string message) : base(message)
        {
        }
    }
}
