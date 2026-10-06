// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.IO;
using System.Text;
using System.Text.Json;

namespace DeviceChain.Sitepulse.Recording
{
    /// <summary>
    /// A recording read back whole: the header, the simulation frames and the three logs, each in the order it was written.
    /// Reading refuses what it cannot trust (a missing file, a version it does not know, a sim.bin cut short, a line that is not
    /// a line) rather than playing what is left. The one thing it forgives is a final ndjson line with no newline that does not
    /// parse: the recorder was killed in the middle of writing it, and the line is dropped with a warning.
    /// </summary>
    public sealed class RecordingData
    {
        public RunHeader Header { get; private set; }
        public SimBinReader Sim { get; private set; }
        public IReadOnlyList<ObservedLine> Observed { get; private set; }
        public IReadOnlyList<DeviceLine> Device { get; private set; }
        public IReadOnlyList<PresenterLine> Presenter { get; private set; }
        public string Directory { get; private set; }
        public List<string> Warnings { get; } = new List<string>();

        /// <summary>The length of the run: the later of the last simulation frame and the last line of any log.</summary>
        public double Duration
        {
            get
            {
                var d = Sim.LastTime;
                if (Observed.Count > 0) d = Math.Max(d, Observed[Observed.Count - 1].T);
                if (Device.Count > 0) d = Math.Max(d, Device[Device.Count - 1].T);
                return d;
            }
        }

        public static RecordingData Load(string directory)
        {
            if (!System.IO.Directory.Exists(directory)) throw new RecordingFormatException("there is no recording at " + directory);
            string Need(string name)
            {
                var p = Path.Combine(directory, name);
                if (!File.Exists(p)) throw new RecordingFormatException($"the recording {directory} has no {name}");
                return p;
            }

            var data = new RecordingData { Directory = directory };
            data.Header = RunHeader.Parse(File.ReadAllText(Need(RecordingFiles.RunJson), Encoding.UTF8));
            data.Sim = SimBinReader.Load(Need(RecordingFiles.SimBin));
            if (data.Sim.FrameCount == 0) throw new RecordingFormatException("sim.bin holds no frames: nothing was recorded");
            data.Observed = ReadLines(Need(RecordingFiles.Observed), RecordingFiles.Observed, ObservedLine.Read, data.Warnings);
            data.Device = ReadLines(Need(RecordingFiles.Device), RecordingFiles.Device, DeviceLine.Read, data.Warnings);
            data.Presenter = ReadLines(Need(RecordingFiles.Presenter), RecordingFiles.Presenter, PresenterLine.Read, data.Warnings);
            if (!data.Header.EndedCleanly)
            {
                data.Warnings.Add("run.json says the run did not end cleanly: the recording may stop short");
                data.RebuildClock();
            }

            return data;
        }

        /// <summary>
        /// A run that did not end cleanly never rewrote its header at the end. The recorder rewrites run.json on every change of the
        /// clock and writes a line to presenter.ndjson, so the segments are the header's plus any the log holds that it lacks. A run that
        /// left no trace of its clock at all cannot say it was real time, and is read as one whose clock is unknown.
        /// </summary>
        void RebuildClock()
        {
            var found = new List<ClockSegment>();
            foreach (var l in Presenter)
            {
                if (l.K != PresenterKinds.Clock || !l.Scale.HasValue) continue;
                var scale = l.Scale.Value;
                found.Add(new ClockSegment { From = l.T, Mode = Math.Abs(scale - 1.0) < 1e-6 ? ClockSegment.Real : ClockSegment.Accelerated, Scale = scale });
            }

            Header.MergeClockSegments(found);
            if (Header.Clock.Count == 0)
            {
                Header.ClockBasis = RunHeader.ClockUnknown;
                Warnings.Add("run.json and presenter.ndjson hold no clock segment of a run that did not end cleanly: the scene's clock speed is unknown, and the footage is treated as accelerated");
            }
            else
            {
                Header.ClockBasis = RunHeader.ClockRebuilt;
                Warnings.Add("the clock segments of a run that did not end cleanly were rebuilt from presenter.ndjson");
            }
        }

        static List<T> ReadLines<T>(string path, string name, Func<JsonElement, T> read, List<string> warnings) where T : RecordLine
        {
            var text = File.ReadAllText(path, Encoding.UTF8);
            var list = new List<T>();
            var start = 0;
            var number = 0;
            double last = double.NegativeInfinity;
            while (start < text.Length)
            {
                var end = text.IndexOf('\n', start);
                var terminated = end >= 0;
                if (!terminated) end = text.Length;
                var line = text.Substring(start, end - start);
                start = end + 1;
                number++;
                if (line.Trim().Length == 0) continue;
                try
                {
                    using var doc = JsonDocument.Parse(line);
                    var item = read(doc.RootElement);
                    if (item.T < last) throw new RecordingFormatException("goes back in time");
                    last = item.T;
                    list.Add(item);
                }
                catch (JsonException e)
                {
                    if (!terminated)
                    {
                        warnings.Add($"{name} ends in a partial line {number}, which was dropped");
                        break;
                    }

                    throw new RecordingFormatException($"{name} line {number} is not JSON: {e.Message}");
                }
                catch (RecordingFormatException e)
                {
                    throw new RecordingFormatException($"{name} line {number}: {e.Message}");
                }
            }

            return list;
        }
    }
}
