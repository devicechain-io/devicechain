// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;

namespace DeviceChain.Sitepulse.Platform
{
    /// <summary>
    /// A deliberate fault the acceptance run injects into ONE launch, so the run can be shown to fail the
    /// right way. Only <c>-sitepulse-acceptance</c> can ask for one, and the run says so everywhere it
    /// reports: the badge, the log and the result file.
    /// </summary>
    public sealed class ControlSpec
    {
        /// <summary>The scene device's external ID is listed on the platform under a name that does not exist there.</summary>
        public const string BogusBinding = "bogus-binding";

        /// <summary>One character of the device's credential is changed after it was looked up.</summary>
        public const string BadCredential = "bad-credential";

        /// <summary>The broker's CA is whatever <c>-dc-ca</c> names; the run is labelled as a control and nothing else changes.</summary>
        public const string WrongCa = "wrong-ca";

        /// <summary>
        /// The script stops the runner while the fleet is up. It changes nothing in the player: the control is a label,
        /// and the run asserts that observation went on (the observer talks to the platform, not the runner).
        /// </summary>
        public const string RunnerStop = "runner-stop";

        /// <summary>
        /// The platform's low-fuel rule is disabled (the script does it, through the platform's own authoring calls, and puts it back).
        /// The player prepares a low tank on SP-HL-0006 and judges that nothing on the platform or in the machine reacted.
        /// </summary>
        public const string RuleDisabled = "rule-disabled";

        /// <summary>
        /// The script scales event-management to zero for a while and back. The player judges what the observer showed: the stream left
        /// Live, the banner, stale cards, devices still publishing, and the snapshot refresh once it was back.
        /// </summary>
        public const string ObserverOutage = "observer-outage";

        public ControlSpec(string kind, string target)
        {
            Kind = kind;
            Target = target;
        }

        public string Kind { get; }

        /// <summary>The scene device the fault is aimed at; null for a fault that is not about one device.</summary>
        public string Target { get; }

        public string Label => Target == null ? Kind : Kind + ":" + Target;

        public static Parsed<ControlSpec> Parse(string text)
        {
            var value = (text ?? "").Trim();
            var colon = value.IndexOf(':');
            var kind = colon < 0 ? value : value.Substring(0, colon);
            var target = colon < 0 ? null : value.Substring(colon + 1);
            switch (kind)
            {
                case BogusBinding:
                case BadCredential:
                    if (string.IsNullOrWhiteSpace(target))
                        return Parsed<ControlSpec>.Fail($"{AcceptanceFlags.ControlFlag} \"{value}\": {kind} needs a device, as {kind}:SP-HL-0004");
                    return Parsed<ControlSpec>.Success(new ControlSpec(kind, target.Trim()));
                case WrongCa:
                case RunnerStop:
                case RuleDisabled:
                case ObserverOutage:
                    if (target != null)
                        return Parsed<ControlSpec>.Fail($"{AcceptanceFlags.ControlFlag} \"{value}\": {kind} takes no device");
                    return Parsed<ControlSpec>.Success(new ControlSpec(kind, null));
                default:
                    return Parsed<ControlSpec>.Fail($"{AcceptanceFlags.ControlFlag} \"{value}\" is not a control; use {BogusBinding}:<device>, {BadCredential}:<device>, {WrongCa}, {RunnerStop}, {RuleDisabled} or {ObserverOutage}");
            }
        }
    }

    /// <summary>What an acceptance launch asked for. Null where there is none: a normal launch has no acceptance.</summary>
    public sealed class AcceptanceOptions
    {
        public const string PhaseA = "phaseA";

        /// <summary>One long Live run with all 18 machines, several real low-fuel cycles, and measurements of the whole.</summary>
        public const string Soak = "soak";

        public const int DefaultSoakMinutes = 30;
        public const int MaxSoakMinutes = 240;

        public string Name { get; set; }

        public bool IsSoak => Name == Soak;

        /// <summary>How long a soak runs once the fleet is observed; <see cref="DefaultSoakMinutes"/> unless asked.</summary>
        public int SoakMinutes { get; set; } = DefaultSoakMinutes;

        /// <summary>Where the result file, the emitted-sample log and the finish flag live; null means the player's persistent data path.</summary>
        public string Directory { get; set; }

        public ControlSpec Control { get; set; }

        public bool IsControl => Control != null;

        /// <summary>The text the badge and the result file carry so a control can never pass for a normal run.</summary>
        public string RunLabel => IsControl ? "ACCEPTANCE CONTROL " + Control.Label : "ACCEPTANCE " + Name;
    }

    /// <summary>
    /// Reads the acceptance flags. They are inert unless <c>-sitepulse-acceptance</c> is given: a control
    /// or a directory without it is an error, so a stray flag can never quietly change what a Live run does.
    /// </summary>
    public static class AcceptanceFlags
    {
        public const string Flag = "-sitepulse-acceptance";
        public const string DirFlag = "-sitepulse-acceptance-dir";
        public const string ControlFlag = "-sitepulse-control";
        public const string SoakFlag = "-sitepulse-soak-minutes";

        /// <summary>Success(null) when acceptance was not requested.</summary>
        public static Parsed<AcceptanceOptions> FromCommandLine(IReadOnlyList<string> args)
        {
            var name = CommandLineArgs.Get(args, Flag, out var e1);
            var dir = CommandLineArgs.Get(args, DirFlag, out var e2);
            var control = CommandLineArgs.Get(args, ControlFlag, out var e3);
            var soak = CommandLineArgs.Get(args, SoakFlag, out var e4);
            var errors = new List<string>();
            foreach (var e in new[] { e1, e2, e3, e4 })
                if (e != null) errors.Add(e);

            if (name == null && e1 == null)
            {
                if (dir != null) errors.Add($"{DirFlag} is given without {Flag}");
                if (control != null) errors.Add($"{ControlFlag} is given without {Flag}: a fault can only be injected by an acceptance run");
                if (soak != null) errors.Add($"{SoakFlag} is given without {Flag}");
                return errors.Count > 0
                    ? Parsed<AcceptanceOptions>.Fail(string.Join("\n", errors))
                    : Parsed<AcceptanceOptions>.Success(null);
            }

            if (name != null && name != AcceptanceOptions.PhaseA && name != AcceptanceOptions.Soak)
                errors.Add($"{Flag} \"{name}\" is not an acceptance; use {AcceptanceOptions.PhaseA} or {AcceptanceOptions.Soak}");

            var minutes = AcceptanceOptions.DefaultSoakMinutes;
            if (soak != null)
            {
                if (name != AcceptanceOptions.Soak) errors.Add($"{SoakFlag} only applies to {Flag} {AcceptanceOptions.Soak}");
                else if (!int.TryParse(soak, System.Globalization.NumberStyles.None, System.Globalization.CultureInfo.InvariantCulture, out minutes) || minutes < 1 || minutes > AcceptanceOptions.MaxSoakMinutes)
                    errors.Add($"{SoakFlag} \"{soak}\" is not a whole number of minutes from 1 to {AcceptanceOptions.MaxSoakMinutes}");
            }

            if (control != null && name == AcceptanceOptions.Soak) errors.Add($"{ControlFlag} is a fault for {AcceptanceOptions.PhaseA}: a soak runs unfaulted");

            ControlSpec spec = null;
            if (control != null)
            {
                var c = ControlSpec.Parse(control);
                if (!c.Ok) errors.Add(c.Error);
                else spec = c.Value;
            }

            if (errors.Count > 0) return Parsed<AcceptanceOptions>.Fail(string.Join("\n", errors));
            return Parsed<AcceptanceOptions>.Success(new AcceptanceOptions { Name = name, Directory = dir, Control = spec, SoakMinutes = minutes });
        }
    }

    /// <summary>The faults themselves. Each is applied only from an <see cref="AcceptanceOptions"/> the app was started with.</summary>
    public static class AcceptanceControls
    {
        /// <summary>What a bogus binding appends to the one external ID it renames.</summary>
        public const string BogusSuffix = "-ACCEPTANCE-BOGUS";

        /// <summary>
        /// The binder's resolve query, asked about <c>target + BogusSuffix</c> where the scene names
        /// <c>target</c>. The platform has no such device, so the target comes back missing and every other
        /// device resolves as usual. Every other query passes through untouched.
        /// </summary>
        public static QueryFn WrapBind(QueryFn inner, ControlSpec control)
        {
            if (inner == null) throw new ArgumentNullException(nameof(inner));
            if (control == null || control.Kind != ControlSpec.BogusBinding) return inner;
            return (query, variablesJson, ct) =>
                inner(query, query == DeviceBinder.ResolveQuery ? RenameId(variablesJson, control.Target) : variablesJson, ct);
        }

        /// <summary>The <c>ids</c> variable with <paramref name="target"/> renamed.</summary>
        public static string RenameId(string variablesJson, string target)
        {
            using var doc = System.Text.Json.JsonDocument.Parse(variablesJson);
            var ids = new List<string>();
            foreach (var v in doc.RootElement.GetProperty("ids").EnumerateArray())
            {
                var s = v.GetString();
                ids.Add(s == target ? s + BogusSuffix : s);
            }

            return VarsJson.StringList("ids", ids);
        }

        /// <summary>Changes one character of the device's credential. False when it has none.</summary>
        public static bool CorruptCredential(DeviceCredentials credentials, string deviceToken)
        {
            if (credentials == null || deviceToken == null || !credentials.TryGet(deviceToken, out var id) || string.IsNullOrEmpty(id)) return false;
            var chars = id.ToCharArray();
            var last = chars.Length - 1;
            chars[last] = chars[last] == '0' ? '1' : '0';
            credentials.Set(deviceToken, new string(chars));
            return true;
        }
    }
}
