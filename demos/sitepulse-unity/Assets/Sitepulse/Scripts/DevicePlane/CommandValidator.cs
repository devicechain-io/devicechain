// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Text.Json;
using DeviceChain.Sitepulse.Domain;

namespace DeviceChain.Sitepulse.DevicePlane
{
    /// <summary>What a command's check found: it can run (and where to), or why it cannot.</summary>
    public readonly struct CommandCheck
    {
        CommandCheck(bool ok, string reason, string area)
        {
            Ok = ok;
            Reason = reason;
            Area = area;
        }

        public bool Ok { get; }
        public string Reason { get; }

        /// <summary>goto-area's destination zone token; null otherwise.</summary>
        public string Area { get; }

        public static CommandCheck Valid(string area = null) => new CommandCheck(true, null, area);
        public static CommandCheck Invalid(string reason) => new CommandCheck(false, reason, null);
    }

    /// <summary>
    /// The first thing a command meets, on the SDK's thread: is it a command this machine has, is its
    /// payload the shape that command takes, and does the destination exist (in the profile's own list of
    /// areas AND in the scene's catalogue of places)? It touches no Unity API and reads the payload with
    /// <see cref="JsonElement"/> only (no serializer), so it is safe off the main thread and under IL2CPP.
    /// A failure is a reason the platform's row will carry. Anything echoed from the command is clipped.
    /// </summary>
    public static class CommandValidator
    {
        public const string AreaParameter = "areaToken";
        public const int EchoLimit = 40;

        public static string Clip(string text)
        {
            if (text == null) return "";
            var sb = new System.Text.StringBuilder();
            foreach (var ch in text)
            {
                if (sb.Length >= EchoLimit) break;
                sb.Append(char.IsControl(ch) ? ' ' : ch);
            }

            return sb.ToString();
        }

        /// <param name="plant">The device is the crusher, which accepts no commands.</param>
        /// <param name="profileAreas">The areas the device's goto-area command accepts (its profile's enum).</param>
        /// <param name="sceneHasZone">Whether the scene has somewhere to send a machine for an area token.</param>
        public static CommandCheck Validate(string name, JsonElement? payload, bool plant, IReadOnlyCollection<string> profileAreas, Func<string, bool> sceneHasZone)
        {
            if (plant) return CommandCheck.Invalid(DeviceSessionHost.PlantRefusal);
            if (string.IsNullOrEmpty(name)) return CommandCheck.Invalid("the command has no name");

            if (name == CommandKeys.GotoRefuel)
            {
                // argument-free: nothing, null or {} only
                if (!payload.HasValue || payload.Value.ValueKind == JsonValueKind.Null || payload.Value.ValueKind == JsonValueKind.Undefined)
                    return CommandCheck.Valid();
                if (payload.Value.ValueKind != JsonValueKind.Object) return CommandCheck.Invalid("goto-refuel takes no parameters, but its payload is not an object");
                foreach (var p in payload.Value.EnumerateObject())
                    return CommandCheck.Invalid($"goto-refuel takes no parameters, but its payload has \"{Clip(p.Name)}\"");
                return CommandCheck.Valid();
            }

            if (name != CommandKeys.GotoArea) return CommandCheck.Invalid($"unknown command \"{Clip(name)}\"");

            if (!payload.HasValue || payload.Value.ValueKind != JsonValueKind.Object)
                return CommandCheck.Invalid($"goto-area needs a payload object with {AreaParameter}");
            var body = payload.Value;
            if (!body.TryGetProperty(AreaParameter, out var area)) return CommandCheck.Invalid($"goto-area needs {AreaParameter}");
            if (area.ValueKind != JsonValueKind.String) return CommandCheck.Invalid($"{AreaParameter} must be a string");
            var token = area.GetString();
            if (string.IsNullOrWhiteSpace(token)) return CommandCheck.Invalid($"{AreaParameter} is empty");
            foreach (var p in body.EnumerateObject())
                if (p.Name != AreaParameter) return CommandCheck.Invalid($"goto-area has an unexpected parameter \"{Clip(p.Name)}\"");

            var listed = false;
            if (profileAreas != null)
                foreach (var a in profileAreas)
                    if (string.Equals(a, token, StringComparison.Ordinal)) { listed = true; break; }
            if (!listed) return CommandCheck.Invalid($"{AreaParameter} \"{Clip(token)}\" is not one of this device's areas");
            if (sceneHasZone == null || !sceneHasZone(token)) return CommandCheck.Invalid($"no scene geometry for area \"{Clip(token)}\"");
            return CommandCheck.Valid(token);
        }
    }
}
