// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Collections.Generic;

namespace DeviceChain.Towerpulse.Model
{
    /// <summary>Static description of one VAV zone. Everything here is a design constant; the live state is <see cref="ZoneState"/>.</summary>
    public sealed class ZoneSpec
    {
        public string Id;
        public string Building;
        public int Floor;
        public string Name;
        /// <summary>Index into <see cref="BuildingLayout.Ahus"/>: the air handler serving the zone.</summary>
        public int Ahu;
        /// <summary>Azimuth the glazing faces, degrees clockwise from north.</summary>
        public double GlassAzimuthDeg;
        public double GlassM2;
        /// <summary>Lumped thermal capacity of air and fit-out, J/K.</summary>
        public double CapacityJPerK;
        /// <summary>Leakage and envelope conductance to the outdoors, W/K.</summary>
        public double LeakWPerK;
        public double VolumeM3;
        /// <summary>People the zone holds at its busiest.</summary>
        public int Capacity;
        /// <summary>Plug and lighting load with the zone occupied, W.</summary>
        public double EquipmentW;
    }

    public sealed class AhuSpec { public string Id; public int Chiller; }
    public sealed class ChillerSpec { public string Id; }
    public sealed class MeterSpec { public string Id; public string Building; public double FloorAreaM2; }

    /// <summary>
    /// The campus: a five-floor tower (four zones a floor) and a two-floor annex (two zones a floor), three air handlers, two chillers and two
    /// energy meters. 24 zones, 3 + 2 + 2 more devices: the 31 of the showcase plan.
    /// </summary>
    public sealed class BuildingLayout
    {
        public const string HotZone = "tower-f4-east";
        public const string AnnexChiller = "chiller-annex";

        public List<ZoneSpec> Zones = new List<ZoneSpec>();
        public List<AhuSpec> Ahus = new List<AhuSpec>();
        public List<ChillerSpec> Chillers = new List<ChillerSpec>();
        public List<MeterSpec> Meters = new List<MeterSpec>();

        public static BuildingLayout Standard()
        {
            var l = new BuildingLayout();
            l.Chillers.Add(new ChillerSpec { Id = "chiller-tower" });
            l.Chillers.Add(new ChillerSpec { Id = AnnexChiller });
            l.Ahus.Add(new AhuSpec { Id = "ahu-tower-low", Chiller = 0 });
            l.Ahus.Add(new AhuSpec { Id = "ahu-tower-high", Chiller = 0 });
            l.Ahus.Add(new AhuSpec { Id = "ahu-annex", Chiller = 1 });
            l.Meters.Add(new MeterSpec { Id = "meter-tower", Building = "tower", FloorAreaM2 = 5 * 4 * 90.0 });
            l.Meters.Add(new MeterSpec { Id = "meter-annex", Building = "annex", FloorAreaM2 = 2 * 2 * 90.0 });

            string[] wings = { "north", "east", "south", "west" };
            // Glazing azimuths. The east wing's glazed return faces south-west, so it takes the afternoon sun: that is why the zone that
            // heats up in the story is the one called east.
            double[] az = { 350, 230, 190, 270 };
            for (int floor = 1; floor <= 5; floor++)
            {
                for (int w = 0; w < 4; w++)
                {
                    bool hot = floor == 4 && w == 1;
                    l.Zones.Add(new ZoneSpec
                    {
                        Id = "tower-f" + floor + "-" + wings[w],
                        Building = "tower",
                        Floor = floor,
                        Name = "Tower F" + floor + " " + char.ToUpperInvariant(wings[w][0]) + wings[w].Substring(1),
                        Ahu = floor <= 3 ? 0 : 1,
                        GlassAzimuthDeg = az[w],
                        GlassM2 = hot ? 3.4 : (floor == 5 ? 2.4 : 2.0),
                        CapacityJPerK = 350000,
                        LeakWPerK = 60,
                        VolumeM3 = hot ? 200 : 300,
                        Capacity = hot ? 6 : 8,
                        EquipmentW = hot ? 400 : 600,
                    });
                }
            }
            for (int floor = 1; floor <= 2; floor++)
            {
                for (int lab = 0; lab < 2; lab++)
                {
                    l.Zones.Add(new ZoneSpec
                    {
                        Id = "annex-f" + floor + "-lab" + (char)('a' + lab),
                        Building = "annex",
                        Floor = floor,
                        Name = "Annex F" + floor + " Lab " + (char)('A' + lab),
                        Ahu = 2,
                        GlassAzimuthDeg = lab == 0 ? 110 : 250,
                        GlassM2 = 2.0,
                        CapacityJPerK = 420000,
                        LeakWPerK = 55,
                        VolumeM3 = 260,
                        Capacity = 6,
                        EquipmentW = 800,
                    });
                }
            }
            return l;
        }

        public int ZoneIndex(string id)
        {
            for (int i = 0; i < Zones.Count; i++) if (Zones[i].Id == id) return i;
            return -1;
        }

        public int ChillerIndex(string id)
        {
            for (int i = 0; i < Chillers.Count; i++) if (Chillers[i].Id == id) return i;
            return -1;
        }

        public int AhuIndex(string id)
        {
            for (int i = 0; i < Ahus.Count; i++) if (Ahus[i].Id == id) return i;
            return -1;
        }
    }

    // ---- live state: public read, internal write. The model is the only thing that moves these.

    public sealed class ZoneState
    {
        public ZoneSpec Spec { get; internal set; }
        public double TempC { get; internal set; }
        public double SetpointC { get; internal set; }
        public double Co2Ppm { get; internal set; }
        public int Occupancy { get; internal set; }
        public double DamperPct { get; internal set; }
        /// <summary>The boost ticket driving the damper, or null.</summary>
        public CommandTicket Boost { get; internal set; }
        /// <summary>Watts of cooling the zone is taking from the air handler right now.</summary>
        public double CoolingW { get; internal set; }
        /// <summary>The damper is being held open by a boost, whether or not its command has completed.</summary>
        public bool BoostOverride { get; internal set; }
        internal double BoostOverrideUntilS;
        internal double ConductanceScale, CapacityScale;
        /// <summary>How many people the schedule (or a hold) wants in the room; Occupancy walks toward it.</summary>
        public int OccupancyTarget { get; internal set; }
    }

    public sealed class AhuState
    {
        public AhuSpec Spec { get; internal set; }
        public double SupplyAirC { get; internal set; }
        public double FanPct { get; internal set; }
        public double FilterDpPa { get; internal set; }
        public AhuMode Mode { get; internal set; }
        public double CoilLoadW { get; internal set; }
    }

    public sealed class ChillerState
    {
        public ChillerSpec Spec { get; internal set; }
        public double ChwSupplyC { get; internal set; }
        public double PowerKw { get; internal set; }
        public double CoolingKw { get; internal set; }
        /// <summary>The compressor is making chilled water.</summary>
        public bool Running { get; internal set; }
        /// <summary>The controller is talking. A tripped chiller is silent: no reports at all, which is what an absence rule sees.</summary>
        public bool Online { get; internal set; }
        internal double RestartLeftS;
        internal CommandTicket Restart;
    }

    public sealed class MeterState
    {
        public MeterSpec Spec { get; internal set; }
        public double Kw { get; internal set; }
        public double Kwh { get; internal set; }
    }
}
