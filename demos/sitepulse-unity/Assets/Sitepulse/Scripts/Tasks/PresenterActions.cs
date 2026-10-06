// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Globalization;
using DeviceChain.Sitepulse.Simulation;

namespace DeviceChain.Sitepulse.Tasks
{
    /// <summary>
    /// What a presenter may do to the demo: change INPUTS of the local simulation, never outputs. None of
    /// these raises a tank, raises an alarm or creates a command; each writes a <c>presenter</c> row to
    /// the machine's timeline so the audience-facing record says it was the presenter's hand.
    /// </summary>
    public static class PresenterActions
    {
        /// <summary>The platform's low-fuel line (sitepulse.go), in percent.</summary>
        public const double LowFuelLinePct = 15.0;

        /// <summary>Where "prepare the next low-fuel cycle" puts the tank: just above the line.</summary>
        public const double JustAbovePct = 15.5;

        /// <summary>About how long the burn takes to cross the line.</summary>
        public const double CrossWithinSeconds = 60.0;

        /// <summary>
        /// Lowers the machine's tank to just above the low-fuel line (never raises it) and adds a burn that
        /// carries it across within about a minute, through the model's normal drain. Returns what it did.
        /// </summary>
        public static string PrepareLowFuel(string id, MachineModel model, Timeline timeline)
        {
            string said;
            if (model.IsPlant) said = "the crusher has no fuel tank";
            else
            {
                var before = model.FuelPct;
                if (model.PrepareLowFuel(JustAbovePct, CrossWithinSeconds, LowFuelLinePct))
                    said = "prepare low-fuel cycle: fuel " + F1(before) + "% -> " + F1(model.FuelPct) + "%, crosses " + F0(LowFuelLinePct) + "% in about " + F0(CrossWithinSeconds) + " s";
                else
                    said = "prepare low-fuel cycle: fuel is already at " + F1(before) + "%, at or below the line: nothing to prepare";
            }

            timeline?.Add(id, TimelineKinds.Presenter, said);
            return said;
        }

        static string F0(double v) => v.ToString("0", CultureInfo.InvariantCulture);
        static string F1(double v) => v.ToString("0.0", CultureInfo.InvariantCulture);
    }
}
