// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.IO;
using System.Text;

namespace DeviceChain.Sitepulse.Recording
{
    public enum SimKind : byte { Dozer = 0, Loader = 1, Hauler = 2 }

    /// <summary>One machine in sim.bin's table: its scene id and what kind of machine it is.</summary>
    public readonly struct SimMachine
    {
        public SimMachine(string id, SimKind kind)
        {
            Id = id;
            Kind = kind;
        }

        public string Id { get; }
        public SimKind Kind { get; }
    }

    /// <summary>
    /// One machine at one instant, exactly as it was rendered and modelled: where it stood (metres, Unity axes; the
    /// angles are degrees, heading clockwise from north), the inputs that posed its rig, what its task layer was doing
    /// and the local model's own values. The pose is what was DRAWN, not what a re-simulation would produce.
    /// </summary>
    public struct MachineSample
    {
        public float X, Y, Z, Heading, Pitch, Roll;

        /// <summary>First implement angle: a dozer's blade arm, a loader's boom, a hauler's dump body.</summary>
        public float P1;

        /// <summary>Second implement angle: a dozer's ripper, a loader's bucket (a hauler has none).</summary>
        public float P2;

        public float Steer;

        /// <summary>Distance the machine has travelled along its heading since the recording began (negative reverses): it spins the wheels.</summary>
        public float Travel;

        public float FuelPct, EngineTempC, EngineHours, PayloadT, TyrePressureKpa;

        /// <summary>The task layer's <c>MachineMode</c> and <c>TaskPhase</c> as numbers.</summary>
        public byte Mode, Phase;

        /// <summary>A hauler carries a load (its rig says so).</summary>
        public bool Loaded;

        /// <summary>The machine is on its routine track (false: a command is driving it).</summary>
        public bool OnTrack;
    }

    /// <summary>
    /// sim.bin, little-endian, no padding between fields:
    /// <code>
    /// header   "SPSB" | u16 version | u16 0 | u32 machineCount | u32 frameBytes | f64 nominalHz
    ///          then machineCount x ( u8 kind | u8 idByteCount | id UTF-8 )
    /// frame    f64 t (seconds since the recording began)
    ///          then machineCount x 64-byte record, in the table's order:
    ///            f32 x y z heading pitch roll | f32 p1 p2 steer travel | f32 fuel engineTemp engineHours payload tyre
    ///            | u8 mode | u8 phase | u8 flags (1 loaded, 2 onTrack) | u8 0
    /// </code>
    /// Frames follow one another to the end of the file; there is no frame count, so a file can be streamed. A file whose
    /// length is not a whole number of frames was cut short, and it is refused, never read as far as it goes.
    /// </summary>
    public static class SimBinLayout
    {
        public const int RecordBytes = 64;
        public const int FixedHeaderBytes = 4 + 2 + 2 + 4 + 4 + 8;
        public const string Magic = "SPSB";
        public const int MaxMachines = 4096;

        public static int FrameBytes(int machines) => 8 + machines * RecordBytes;

        internal static void CheckEndianness()
        {
            if (!BitConverter.IsLittleEndian) throw new PlatformNotSupportedException("sim.bin is little-endian and this host is not");
        }

        internal static void Put(byte[] b, ref int o, float v)
        {
            BitConverter.TryWriteBytes(new Span<byte>(b, o, 4), v);
            o += 4;
        }

        internal static float GetF(byte[] b, int o) => BitConverter.ToSingle(b, o);
    }

    /// <summary>Streams frames to a file. One reusable buffer; the memory it holds does not grow with the length of the run.</summary>
    public sealed class SimBinWriter : IDisposable
    {
        readonly Stream stream;
        readonly byte[] buffer;
        readonly int machines;
        double lastT = double.NegativeInfinity;

        public SimBinWriter(Stream stream, IReadOnlyList<SimMachine> table, double nominalHz)
        {
            SimBinLayout.CheckEndianness();
            this.stream = stream ?? throw new ArgumentNullException(nameof(stream));
            if (table == null || table.Count == 0) throw new ArgumentException("a recording needs at least one machine", nameof(table));
            if (table.Count > SimBinLayout.MaxMachines) throw new ArgumentException("too many machines", nameof(table));
            machines = table.Count;
            buffer = new byte[SimBinLayout.FrameBytes(machines)];
            WriteHeader(table, nominalHz);
        }

        public long BytesWritten { get; private set; }
        public int Frames { get; private set; }

        void WriteHeader(IReadOnlyList<SimMachine> table, double hz)
        {
            using var ms = new MemoryStream();
            ms.Write(Encoding.ASCII.GetBytes(SimBinLayout.Magic), 0, 4);
            ms.Write(BitConverter.GetBytes((ushort)RecordingFormat.Version), 0, 2);
            ms.Write(BitConverter.GetBytes((ushort)0), 0, 2);
            ms.Write(BitConverter.GetBytes((uint)machines), 0, 4);
            ms.Write(BitConverter.GetBytes((uint)buffer.Length), 0, 4);
            ms.Write(BitConverter.GetBytes(hz), 0, 8);
            foreach (var m in table)
            {
                var id = Encoding.UTF8.GetBytes(m.Id ?? "");
                if (id.Length == 0 || id.Length > 255) throw new ArgumentException("a machine id must be 1 to 255 bytes: " + m.Id);
                ms.WriteByte((byte)m.Kind);
                ms.WriteByte((byte)id.Length);
                ms.Write(id, 0, id.Length);
            }

            var bytes = ms.ToArray();
            stream.Write(bytes, 0, bytes.Length);
            BytesWritten += bytes.Length;
        }

        /// <summary>Writes one frame as a single write, so a crash can cut the file only between whole frames or inside the last one.</summary>
        public void WriteFrame(double t, MachineSample[] samples)
        {
            if (samples == null || samples.Length != machines) throw new ArgumentException($"a frame holds {machines} machines, not {samples?.Length ?? 0}", nameof(samples));
            if (double.IsNaN(t) || t < lastT) throw new ArgumentException($"frame time {t} does not follow {lastT}", nameof(t));
            lastT = t;
            BitConverter.TryWriteBytes(new Span<byte>(buffer, 0, 8), t);
            var o = 8;
            for (var i = 0; i < machines; i++)
            {
                ref var s = ref samples[i];
                SimBinLayout.Put(buffer, ref o, s.X); SimBinLayout.Put(buffer, ref o, s.Y); SimBinLayout.Put(buffer, ref o, s.Z);
                SimBinLayout.Put(buffer, ref o, s.Heading); SimBinLayout.Put(buffer, ref o, s.Pitch); SimBinLayout.Put(buffer, ref o, s.Roll);
                SimBinLayout.Put(buffer, ref o, s.P1); SimBinLayout.Put(buffer, ref o, s.P2); SimBinLayout.Put(buffer, ref o, s.Steer); SimBinLayout.Put(buffer, ref o, s.Travel);
                SimBinLayout.Put(buffer, ref o, s.FuelPct); SimBinLayout.Put(buffer, ref o, s.EngineTempC); SimBinLayout.Put(buffer, ref o, s.EngineHours);
                SimBinLayout.Put(buffer, ref o, s.PayloadT); SimBinLayout.Put(buffer, ref o, s.TyrePressureKpa);
                buffer[o++] = s.Mode;
                buffer[o++] = s.Phase;
                buffer[o++] = (byte)((s.Loaded ? 1 : 0) | (s.OnTrack ? 2 : 0));
                buffer[o++] = 0;
            }

            stream.Write(buffer, 0, buffer.Length);
            BytesWritten += buffer.Length;
            Frames++;
        }

        public void Flush() => stream.Flush();

        public void Dispose() => stream.Dispose();
    }

    /// <summary>A sim.bin held in memory, read by position. Refuses a file that is not whole.</summary>
    public sealed class SimBinReader
    {
        /// <summary>Two frames further apart than this are not interpolated across: the recorder was not running between them.</summary>
        public const double MaxInterpolationGap = 0.5;

        readonly byte[] data;
        readonly int headerBytes, frameBytes;
        readonly List<SimMachine> machines = new List<SimMachine>();
        readonly Dictionary<string, int> index = new Dictionary<string, int>(StringComparer.Ordinal);

        SimBinReader(byte[] data, int headerBytes, int frameBytes, int frames, double hz)
        {
            this.data = data;
            this.headerBytes = headerBytes;
            this.frameBytes = frameBytes;
            FrameCount = frames;
            NominalHz = hz;
        }

        public IReadOnlyList<SimMachine> Machines => machines;
        public int FrameCount { get; }
        public double NominalHz { get; }
        public int Bytes => data.Length;

        public double FirstTime => FrameCount == 0 ? 0.0 : TimeOf(0);
        public double LastTime => FrameCount == 0 ? 0.0 : TimeOf(FrameCount - 1);

        public bool TryIndexOf(string id, out int i) => index.TryGetValue(id, out i);

        public double TimeOf(int frame) => BitConverter.ToDouble(data, headerBytes + frame * frameBytes);

        public static SimBinReader Parse(byte[] data)
        {
            SimBinLayout.CheckEndianness();
            if (data == null || data.Length < SimBinLayout.FixedHeaderBytes) throw new RecordingFormatException("sim.bin is shorter than its own header: the file was cut short or is not a sim.bin");
            if (Encoding.ASCII.GetString(data, 0, 4) != SimBinLayout.Magic) throw new RecordingFormatException("sim.bin does not start with the SPSB marker: it is not a sim.bin");
            var version = BitConverter.ToUInt16(data, 4);
            if (version != RecordingFormat.Version) throw new RecordingFormatException($"sim.bin is version {version}; this build reads version {RecordingFormat.Version}");
            var count = BitConverter.ToUInt32(data, 8);
            var declared = BitConverter.ToUInt32(data, 12);
            var hz = BitConverter.ToDouble(data, 16);
            if (count == 0 || count > SimBinLayout.MaxMachines) throw new RecordingFormatException($"sim.bin names {count} machines");
            if (declared != SimBinLayout.FrameBytes((int)count)) throw new RecordingFormatException($"sim.bin declares {declared}-byte frames for {count} machines, which is not this layout");
            var o = SimBinLayout.FixedHeaderBytes;
            var table = new List<SimMachine>();
            for (var i = 0; i < count; i++)
            {
                if (o + 2 > data.Length) throw new RecordingFormatException("sim.bin was cut short inside its machine table");
                var kind = data[o++];
                var len = data[o++];
                if (kind > (byte)SimKind.Hauler || len == 0 || o + len > data.Length) throw new RecordingFormatException("sim.bin has a malformed machine table");
                table.Add(new SimMachine(Encoding.UTF8.GetString(data, o, len), (SimKind)kind));
                o += len;
            }

            var body = data.Length - o;
            var frameBytes = (int)declared;
            if (body % frameBytes != 0)
                throw new RecordingFormatException($"sim.bin was cut short: {body} bytes of frames is not a whole number of {frameBytes}-byte frames ({body % frameBytes} bytes of a partial frame)");
            var reader = new SimBinReader(data, o, frameBytes, body / frameBytes, hz);
            foreach (var m in table)
            {
                if (reader.index.ContainsKey(m.Id)) throw new RecordingFormatException("sim.bin names machine " + m.Id + " twice");
                reader.index[m.Id] = reader.machines.Count;
                reader.machines.Add(m);
            }

            for (var f = 1; f < reader.FrameCount; f++)
                if (reader.TimeOf(f) < reader.TimeOf(f - 1)) throw new RecordingFormatException($"sim.bin frame {f} goes back in time");
            return reader;
        }

        public static SimBinReader Load(string path)
        {
            byte[] bytes;
            try { bytes = File.ReadAllBytes(path); }
            catch (IOException e) { throw new RecordingFormatException($"{path} cannot be read: {e.Message}"); }
            return Parse(bytes);
        }

        public MachineSample Frame(int frame, int machine)
        {
            var o = headerBytes + frame * frameBytes + 8 + machine * SimBinLayout.RecordBytes;
            var s = new MachineSample
            {
                X = SimBinLayout.GetF(data, o), Y = SimBinLayout.GetF(data, o + 4), Z = SimBinLayout.GetF(data, o + 8),
                Heading = SimBinLayout.GetF(data, o + 12), Pitch = SimBinLayout.GetF(data, o + 16), Roll = SimBinLayout.GetF(data, o + 20),
                P1 = SimBinLayout.GetF(data, o + 24), P2 = SimBinLayout.GetF(data, o + 28), Steer = SimBinLayout.GetF(data, o + 32), Travel = SimBinLayout.GetF(data, o + 36),
                FuelPct = SimBinLayout.GetF(data, o + 40), EngineTempC = SimBinLayout.GetF(data, o + 44), EngineHours = SimBinLayout.GetF(data, o + 48),
                PayloadT = SimBinLayout.GetF(data, o + 52), TyrePressureKpa = SimBinLayout.GetF(data, o + 56),
                Mode = data[o + 60], Phase = data[o + 61],
            };
            var flags = data[o + 62];
            s.Loaded = (flags & 1) != 0;
            s.OnTrack = (flags & 2) != 0;
            return s;
        }

        /// <summary>The last frame at or before <paramref name="t"/> (0 when t is before the first).</summary>
        public int FrameAtOrBefore(double t)
        {
            int lo = 0, hi = FrameCount - 1;
            if (hi < 0) return -1;
            if (t < TimeOf(0)) return 0;
            while (lo < hi)
            {
                var mid = (lo + hi + 1) >> 1;
                if (TimeOf(mid) <= t) lo = mid;
                else hi = mid - 1;
            }

            return lo;
        }

        /// <summary>
        /// The machine at time <paramref name="t"/>: positions and angles interpolated between the frames either side (angles by
        /// the shortest way round), everything else as of the earlier frame. Before the first frame it is the first, after the
        /// last it is the last, and across a gap longer than <see cref="MaxInterpolationGap"/> it holds the earlier one.
        /// </summary>
        public MachineSample Sample(double t, int machine)
        {
            var i = FrameAtOrBefore(t);
            if (i < 0) throw new InvalidOperationException("the recording holds no frames");
            var a = Frame(i, machine);
            if (i + 1 >= FrameCount || t <= TimeOf(i)) return a;
            var t0 = TimeOf(i);
            var t1 = TimeOf(i + 1);
            if (t1 - t0 > MaxInterpolationGap || t1 <= t0) return a;
            var b = Frame(i + 1, machine);
            var w = (float)((t - t0) / (t1 - t0));
            var r = a;
            r.X = Lerp(a.X, b.X, w); r.Y = Lerp(a.Y, b.Y, w); r.Z = Lerp(a.Z, b.Z, w);
            r.Heading = Angle(a.Heading, b.Heading, w); r.Pitch = Angle(a.Pitch, b.Pitch, w); r.Roll = Angle(a.Roll, b.Roll, w);
            r.P1 = Lerp(a.P1, b.P1, w); r.P2 = Lerp(a.P2, b.P2, w); r.Steer = Lerp(a.Steer, b.Steer, w); r.Travel = Lerp(a.Travel, b.Travel, w);
            r.FuelPct = Lerp(a.FuelPct, b.FuelPct, w); r.EngineTempC = Lerp(a.EngineTempC, b.EngineTempC, w); r.EngineHours = Lerp(a.EngineHours, b.EngineHours, w);
            r.PayloadT = Lerp(a.PayloadT, b.PayloadT, w); r.TyrePressureKpa = Lerp(a.TyrePressureKpa, b.TyrePressureKpa, w);
            return r;
        }

        static float Lerp(float a, float b, float w) => a + (b - a) * w;

        static float Angle(float a, float b, float w)
        {
            var d = (b - a) % 360f;
            if (d > 180f) d -= 360f;
            else if (d < -180f) d += 360f;
            return a + d * w;
        }
    }
}
