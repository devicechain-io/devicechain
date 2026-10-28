// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;

namespace DeviceChain.Towerpulse.Model
{
    /// <summary>
    /// The campus, with no engine in it: a first-order lumped thermal model per zone, a CO2 mass balance, three air handlers, two chillers and two
    /// meters, advanced in fixed 5 s steps from a seed. Same seed and same calls give the same trace on every runtime, so the dotnet tests and the
    /// Editor see one program. The model never decides anything a platform rule would: it only reacts to commands.
    ///
    /// Zone: C dT/dt = solar + people + equipment + leakage(Tout - T) - cooling, with cooling = Kmax * damper * max(0, T - supply air).
    /// CO2:  dC/dt = people * generation / volume - airChanges(damper) * (C - outdoor).
    /// </summary>
    public sealed class BuildingModel
    {
        public const double StepS = 5.0;
        public const double PeakSolarWPerM2 = 450.0;
        public const double PersonHeatW = 100.0;
        /// <summary>Cooling at a fully open damper, per kelvin between the room and the supply air, W/K.</summary>
        public const double CoolingWPerK = 400.0;
        /// <summary>A normal (unboosted) VAV box is limited to this much of its travel: its design airflow, with headroom held back for boost.</summary>
        public const double NormalDamperCapPct = 55.0;
        /// <summary>Damper opening per kelvin above setpoint, in the normal (unboosted) control.</summary>
        public const double ProportionalGainPctPerK = 40.0;
        /// <summary>After a boost's command completes the damper stays open until the room is this far BELOW setpoint (or the window ends), so it does not creep straight back.</summary>
        public const double BoostHoldDownC = 2.0;
        public const double DefaultSetpointC = 24.0;
        public const double MinSetpointC = 16.0, MaxSetpointC = 30.0;
        /// <summary>A boost is done when the room is within this of its setpoint.</summary>
        public const double SetpointToleranceC = 0.05;
        public const double MaxBoostMinutes = 240.0;
        public const double RestartDelayS = 120.0;
        public const double OutdoorCo2Ppm = 420.0;
        public const double CompleteChwC = 7.5;

        readonly DeterministicRng _rng;
        readonly ulong _seed;
        int _nextTicket = 1;
        double _nextOccupancyTickS;

        public BuildingLayout Layout { get; private set; }
        public double TimeS { get; private set; }
        public double OutdoorC { get; private set; }
        /// <summary>Added to the outdoor temperature: the script makes the story day a hot one.</summary>
        public double OutdoorBiasC { get; set; }
        public IReadOnlyList<ZoneState> Zones { get { return _zones; } }
        public IReadOnlyList<AhuState> Ahus { get { return _ahus; } }
        public IReadOnlyList<ChillerState> Chillers { get { return _chillers; } }
        public IReadOnlyList<MeterState> Meters { get { return _meters; } }
        public IReadOnlyList<ModelEvent> Events { get { return _events; } }
        public IReadOnlyList<CommandTicket> Tickets { get { return _tickets; } }

        readonly List<ZoneState> _zones = new List<ZoneState>();
        readonly List<AhuState> _ahus = new List<AhuState>();
        readonly List<ChillerState> _chillers = new List<ChillerState>();
        readonly List<MeterState> _meters = new List<MeterState>();
        readonly List<ModelEvent> _events = new List<ModelEvent>();
        readonly List<CommandTicket> _tickets = new List<CommandTicket>();
        readonly List<Hold> _holds = new List<Hold>();

        sealed class Hold { public int Zone; public double StartS, EndS; public int Count; }

        public BuildingModel(ulong seed, double startS = 6 * 3600.0, BuildingLayout layout = null)
        {
            _seed = seed;
            _rng = new DeterministicRng(seed);
            Layout = layout ?? BuildingLayout.Standard();
            TimeS = startS;
            _nextOccupancyTickS = startS;
            OutdoorC = ComputeOutdoor(startS);

            for (int i = 0; i < Layout.Zones.Count; i++)
            {
                var s = Layout.Zones[i];
                bool hot = s.Id == BuildingLayout.HotZone;
                // Real rooms differ. The one the story is about varies least, so its beat does not hinge on a lucky seed.
                double spread = hot ? 0.03 : 0.10;
                _zones.Add(new ZoneState
                {
                    Spec = s,
                    TempC = 23.0 + 0.6 * (DeterministicRng.Hash01(seed, i, 21) - 0.5),
                    SetpointC = DefaultSetpointC,
                    Co2Ppm = 450,
                    Occupancy = 0,
                    DamperPct = 5,
                    ConductanceScale = 1 + spread * (2 * DeterministicRng.Hash01(seed, i, 22) - 1),
                    CapacityScale = 1 + spread * (2 * DeterministicRng.Hash01(seed, i, 23) - 1),
                });
            }
            for (int i = 0; i < Layout.Ahus.Count; i++)
                _ahus.Add(new AhuState { Spec = Layout.Ahus[i], SupplyAirC = 13, FanPct = 20, Mode = AhuMode.Normal, FilterDpPa = 140 + 20 * DeterministicRng.Hash01(seed, i, 31) });
            for (int i = 0; i < Layout.Chillers.Count; i++)
                _chillers.Add(new ChillerState { Spec = Layout.Chillers[i], ChwSupplyC = 6.5, Running = true, Online = true });
            for (int i = 0; i < Layout.Meters.Count; i++)
                _meters.Add(new MeterState { Spec = Layout.Meters[i] });
        }

        // ---- the day

        /// <summary>Whether the building is conditioned for occupancy: weekdays 07:00 to 19:00.</summary>
        public static bool InOccupiedHours(double simS)
        {
            double h = TimeClock.Hours(simS);
            return TimeClock.IsWeekday(simS) && h >= 7.0 && h < 19.0;
        }

        double ComputeOutdoor(double simS)
        {
            double h = TimeClock.Hours(simS);
            int day = TimeClock.DayIndex(simS);
            double mean = 23.0 + 3.0 * (2 * DeterministicRng.Hash01(_seed, day, 1) - 1);
            return mean + OutdoorBiasC + 8.0 * Math.Sin(2 * Math.PI * (h - 9.0) / 24.0);
        }

        /// <summary>Solar heat into a zone, W: glazing area, the height of the sun, how squarely the glass faces it, and a clear-sky factor for the day.</summary>
        public double SolarGainW(ZoneSpec z, double simS)
        {
            double h = TimeClock.Hours(simS);
            if (h <= 6.0 || h >= 18.0) return 0;
            double elevation = Math.Sin(Math.PI * (h - 6.0) / 12.0);
            double sunAz = 90.0 + 180.0 * (h - 6.0) / 12.0;
            double facing = Math.Cos((sunAz - z.GlassAzimuthDeg) * Math.PI / 180.0);
            if (facing < 0) facing = 0;
            double clear = 0.8 + 0.2 * DeterministicRng.Hash01(_seed, TimeClock.DayIndex(simS), 7);
            return z.GlassM2 * PeakSolarWPerM2 * facing * elevation * clear;
        }

        /// <summary>Make a room hold <paramref name="count"/> people between two sim times, whatever the ordinary schedule says: the meeting.</summary>
        public void HoldOccupancy(string zoneId, double startS, double endS, int count)
        {
            int zi = Layout.ZoneIndex(zoneId);
            if (zi < 0) throw new ArgumentException("no zone " + zoneId);
            _holds.Add(new Hold { Zone = zi, StartS = startS, EndS = endS, Count = count });
        }

        int OccupancyTargetFor(int zi, double simS)
        {
            for (int i = 0; i < _holds.Count; i++)
            {
                var hd = _holds[i];
                if (hd.Zone == zi && simS >= hd.StartS && simS < hd.EndS) return hd.Count;
            }
            if (!TimeClock.IsWeekday(simS)) return 0;
            double h = TimeClock.Hours(simS);
            double arrive = 8.0 + 1.3 * DeterministicRng.Hash01(_seed, zi, 11);
            double leave = 17.0 + 1.0 * DeterministicRng.Hash01(_seed, zi, 12);
            double up = Clamp01((h - arrive) / 0.5);
            double down = 1.0 - Clamp01((h - leave) / 0.5);
            double lunch = (h >= 12.25 && h < 13.0) ? 0.55 : 1.0;
            return (int)Math.Round(_zones[zi].Spec.Capacity * 0.85 * up * down * lunch);
        }

        static double Clamp01(double v) { return v < 0 ? 0 : (v > 1 ? 1 : v); }
        static double Clamp(double v, double lo, double hi) { return v < lo ? lo : (v > hi ? hi : v); }

        // ---- time

        /// <summary>Run the model forward until <paramref name="simS"/>. Time only moves forward, in whole steps.</summary>
        public void AdvanceTo(double simS)
        {
            while (TimeS + StepS <= simS + 1e-9) Step();
        }

        void Step()
        {
            double dt = StepS;
            double t = TimeS + dt;
            TimeS = t;
            OutdoorC = ComputeOutdoor(t);
            bool occupiedHours = InOccupiedHours(t);

            if (t >= _nextOccupancyTickS)
            {
                _nextOccupancyTickS = t + 30.0;
                for (int i = 0; i < _zones.Count; i++)
                {
                    var z = _zones[i];
                    z.OccupancyTarget = OccupancyTargetFor(i, t);
                    int diff = z.OccupancyTarget - z.Occupancy;
                    if (diff != 0 && _rng.NextDouble() < 0.7)
                    {
                        int move = Math.Min(Math.Abs(diff), 2);
                        z.Occupancy += diff > 0 ? move : -move;
                    }
                }
            }

            // Chillers first: the chilled water the air handlers see this step.
            for (int i = 0; i < _chillers.Count; i++)
            {
                var c = _chillers[i];
                if (c.RestartLeftS > 0)
                {
                    c.RestartLeftS -= dt;
                    if (c.RestartLeftS <= 0) { c.RestartLeftS = 0; c.Running = true; Log("chiller-running", c.Spec.Id, ""); }
                }
                double target = c.Running ? 6.5 : 14.0;
                c.ChwSupplyC += (target - c.ChwSupplyC) * (1 - Math.Exp(-dt / 240.0));
                if (c.Restart != null && c.Running && c.ChwSupplyC <= CompleteChwC) Finish(c.Restart, CommandState.Completed, "");
                if (c.Restart != null && c.Restart.IsFinal) c.Restart = null;
            }

            for (int i = 0; i < _ahus.Count; i++)
            {
                var a = _ahus[i];
                var ch = _chillers[a.Spec.Chiller];
                double modeC = a.Mode == AhuMode.Economy ? 15.0 : (a.Mode == AhuMode.Boost ? 11.5 : 13.0);
                double target = modeC + 0.9 * Math.Max(0, ch.ChwSupplyC - 7.0);
                a.SupplyAirC += (target - a.SupplyAirC) * (1 - Math.Exp(-dt / 60.0));
            }

            for (int i = 0; i < _ahus.Count; i++) _ahus[i].CoilLoadW = 0;
            for (int i = 0; i < _zones.Count; i++) StepZone(_zones[i], t, dt, occupiedHours);

            for (int i = 0; i < _ahus.Count; i++)
            {
                var a = _ahus[i];
                double sum = 0; int n = 0;
                for (int k = 0; k < _zones.Count; k++) if (_zones[k].Spec.Ahu == i) { sum += _zones[k].DamperPct; n++; }
                double mean = n == 0 ? 0 : sum / n;
                double fan = occupiedHours ? 30.0 + 0.7 * mean : 20.0;
                if (a.Mode == AhuMode.Economy) fan -= 8.0; else if (a.Mode == AhuMode.Boost) fan += 20.0;
                fan = Clamp(fan, 0, 100);
                a.FanPct += (fan - a.FanPct) * (1 - Math.Exp(-dt / 30.0));
                // Dirt loads the filter faster the harder the fan pulls; it only ever rises, until somebody changes it.
                double r = a.FanPct / 50.0;
                a.FilterDpPa += 3.0 * r * r * dt / 3600.0;
            }

            for (int i = 0; i < _chillers.Count; i++)
            {
                var c = _chillers[i];
                double load = 0;
                for (int k = 0; k < _ahus.Count; k++) if (_ahus[k].Spec.Chiller == i) load += _ahus[k].CoilLoadW;
                double cop = Clamp(5.2 - 0.07 * (OutdoorC - 20.0), 2.5, 6.0);
                if (c.Running) { c.CoolingKw = load / 1000.0; c.PowerKw = c.CoolingKw / cop + 6.0; }
                else { c.CoolingKw = 0; c.PowerKw = c.Online ? 0.3 : 0.0; }
            }

            for (int m = 0; m < _meters.Count; m++)
            {
                var me = _meters[m];
                string building = Layout.Meters[m].Building;
                double kw = 1.5;
                for (int i = 0; i < _chillers.Count; i++)
                    if (building == (Layout.Chillers[i].Id == BuildingLayout.AnnexChiller ? "annex" : "tower")) kw += _chillers[i].PowerKw;
                for (int i = 0; i < _ahus.Count; i++)
                    if (building == (Layout.Ahus[i].Chiller == 1 ? "annex" : "tower")) { double f = _ahus[i].FanPct / 100.0; kw += 7.5 * f * f * f; }
                for (int i = 0; i < _zones.Count; i++)
                    if (_zones[i].Spec.Building == building) kw += EquipmentW(_zones[i]) / 1000.0;
                me.Kw = kw;
                me.Kwh += kw * dt / 3600.0;
            }
        }

        /// <summary>Plug load. Lab kit trips itself off in a room that has gone far too hot (full below 32 C, none from 40 C), so a dead chiller cannot cook a room without limit.</summary>
        static double EquipmentW(ZoneState z) { return z.Spec.EquipmentW * (z.Occupancy > 0 ? 1.0 : 0.25) * Clamp((40.0 - z.TempC) / 8.0, 0, 1); }

        void StepZone(ZoneState z, double t, double dt, bool occupiedHours)
        {
            var s = z.Spec;
            var ahu = _ahus[s.Ahu];

            double cmd;
            if (z.BoostOverride) cmd = 100.0;
            else if (occupiedHours) cmd = Clamp(12.0 + ProportionalGainPctPerK * (z.TempC - z.SetpointC), 12.0, NormalDamperCapPct);
            else cmd = z.TempC > z.SetpointC + 4.0 ? Clamp(12.0 + ProportionalGainPctPerK * (z.TempC - z.SetpointC - 4.0), 12.0, NormalDamperCapPct) : 5.0;
            double slew = 2.0 * dt;
            z.DamperPct = Clamp(z.DamperPct + Clamp(cmd - z.DamperPct, -slew, slew), 0, 100);

            // Blinds close on a hot room: full sun below 30 C, a fifth of it from 38 C.
            double solar = SolarGainW(s, t) * Clamp(1.0 - (z.TempC - 30.0) / 10.0, 0.2, 1.0);
            double people = PersonHeatW * z.Occupancy;
            double leak = s.LeakWPerK * z.ConductanceScale * (OutdoorC - z.TempC);
            double cooling = CoolingWPerK * (z.DamperPct / 100.0) * Math.Max(0, z.TempC - ahu.SupplyAirC);
            z.CoolingW = cooling;
            ahu.CoilLoadW += cooling;
            z.TempC += (solar + people + EquipmentW(z) + leak - cooling) * dt / (s.CapacityJPerK * z.CapacityScale);

            double ach = 0.6 + 5.4 * z.DamperPct / 100.0;
            double gen = z.Occupancy * 5.2 / s.VolumeM3;
            z.Co2Ppm += (gen - ach / 3600.0 * (z.Co2Ppm - OutdoorCo2Ppm)) * dt;

            if (z.BoostOverride && (t >= z.BoostOverrideUntilS || (z.Boost == null && z.TempC <= z.SetpointC - BoostHoldDownC))) z.BoostOverride = false;
            var b = z.Boost;
            if (b != null)
            {
                // The completion condition: the room is at its setpoint. Not "the damper opened", not "the minutes elapsed".
                if (z.TempC <= z.SetpointC + SetpointToleranceC) { Finish(b, CommandState.Completed, ""); z.Boost = null; }
                else if (t - b.IssuedAtS >= b.WindowS) { Finish(b, CommandState.Expired, "window elapsed before the zone reached setpoint"); z.Boost = null; z.BoostOverride = false; }
            }
        }

        // ---- commands

        CommandTicket NewTicket(CommandKind kind, string target)
        {
            var tk = new CommandTicket { Id = _nextTicket++, Kind = kind, Target = target, State = CommandState.Active, IssuedAtS = TimeS };
            _tickets.Add(tk);
            Log("command", target, kind.ToString());
            return tk;
        }

        void Finish(CommandTicket tk, CommandState state, string reason)
        {
            tk.State = state; tk.Reason = reason; tk.EndedAtS = TimeS;
            Log("command-" + state.ToString().ToLowerInvariant(), tk.Target, tk.Kind + (reason.Length > 0 ? " " + reason : ""));
        }

        CommandTicket Rejected(CommandKind kind, string target, string reason)
        {
            var tk = NewTicket(kind, target);
            Finish(tk, CommandState.Failed, reason);
            return tk;
        }

        /// <summary>Open the damper fully for up to <paramref name="minutes"/>. Completes when the zone reaches its setpoint; Expires if it does not get there in time.</summary>
        public CommandTicket BoostCooling(string zoneId, double minutes)
        {
            int zi = Layout.ZoneIndex(zoneId);
            if (zi < 0) return Rejected(CommandKind.BoostCooling, zoneId, "unknown zone");
            if (!(minutes > 0) || minutes > MaxBoostMinutes) return Rejected(CommandKind.BoostCooling, zoneId, "minutes must be in (0, " + MaxBoostMinutes + "]");
            var z = _zones[zi];
            if (z.Boost != null) Finish(z.Boost, CommandState.Failed, "superseded by a newer boost");
            var tk = NewTicket(CommandKind.BoostCooling, zoneId);
            tk.WindowS = minutes * 60.0;
            z.Boost = tk;
            z.BoostOverride = true;
            z.BoostOverrideUntilS = TimeS + tk.WindowS;
            return tk;
        }

        public CommandTicket SetSetpoint(string zoneId, double celsius)
        {
            int zi = Layout.ZoneIndex(zoneId);
            if (zi < 0) return Rejected(CommandKind.SetSetpoint, zoneId, "unknown zone");
            if (!(celsius >= MinSetpointC && celsius <= MaxSetpointC)) return Rejected(CommandKind.SetSetpoint, zoneId, "setpoint must be in [" + MinSetpointC + ", " + MaxSetpointC + "]");
            _zones[zi].SetpointC = celsius;
            var tk = NewTicket(CommandKind.SetSetpoint, zoneId);
            Finish(tk, CommandState.Completed, "");
            return tk;
        }

        public CommandTicket SetAhuMode(string ahuId, string mode)
        {
            AhuMode m;
            if (mode == "normal") m = AhuMode.Normal;
            else if (mode == "economy") m = AhuMode.Economy;
            else if (mode == "boost") m = AhuMode.Boost;
            else return Rejected(CommandKind.SetAhuMode, ahuId, "mode must be normal, economy or boost");
            int ai = Layout.AhuIndex(ahuId);
            if (ai < 0) return Rejected(CommandKind.SetAhuMode, ahuId, "unknown air handler");
            _ahus[ai].Mode = m;
            var tk = NewTicket(CommandKind.SetAhuMode, ahuId);
            Finish(tk, CommandState.Completed, "");
            return tk;
        }

        /// <summary>
        /// Cycle a chiller. It comes back after <see cref="RestartDelayS"/>, and the command completes once it is running AND its chilled water is cold
        /// again, so a "restarted" chiller still pulling warm water has not recovered. A tripped chiller is silent, but its watchdog takes the restart.
        /// </summary>
        public CommandTicket RestartChiller(string chillerId)
        {
            int ci = Layout.ChillerIndex(chillerId);
            if (ci < 0) return Rejected(CommandKind.RestartChiller, chillerId, "unknown chiller");
            var c = _chillers[ci];
            if (c.Restart != null) return Rejected(CommandKind.RestartChiller, chillerId, "already restarting");
            var tk = NewTicket(CommandKind.RestartChiller, chillerId);
            c.Restart = tk;
            c.Online = true;
            c.Running = false;
            c.RestartLeftS = RestartDelayS;
            return tk;
        }

        /// <summary>A fault, not a command: the chiller stops and goes silent.</summary>
        public void TripChiller(string chillerId)
        {
            int ci = Layout.ChillerIndex(chillerId);
            if (ci < 0) throw new ArgumentException("no chiller " + chillerId);
            var c = _chillers[ci];
            c.Running = false; c.Online = false; c.RestartLeftS = 0;
            if (c.Restart != null) { Finish(c.Restart, CommandState.Failed, "chiller tripped during restart"); c.Restart = null; }
            Log("chiller-tripped", chillerId, "");
        }

        void Log(string kind, string subject, string detail)
        {
            _events.Add(new ModelEvent { TimeS = TimeS, Kind = kind, Subject = subject, Detail = detail });
        }

        /// <summary>A flat copy of everything a device would report, for traces and determinism checks.</summary>
        public double[] Snapshot()
        {
            var l = new List<double>();
            l.Add(TimeS); l.Add(OutdoorC);
            foreach (var z in _zones) { l.Add(z.TempC); l.Add(z.SetpointC); l.Add(z.Co2Ppm); l.Add(z.Occupancy); l.Add(z.DamperPct); }
            foreach (var a in _ahus) { l.Add(a.SupplyAirC); l.Add(a.FanPct); l.Add(a.FilterDpPa); l.Add((double)a.Mode); }
            foreach (var c in _chillers) { l.Add(c.ChwSupplyC); l.Add(c.PowerKw); l.Add(c.CoolingKw); l.Add(c.Running ? 1 : 0); l.Add(c.Online ? 1 : 0); }
            foreach (var m in _meters) { l.Add(m.Kw); l.Add(m.Kwh); }
            return l.ToArray();
        }
    }
}
