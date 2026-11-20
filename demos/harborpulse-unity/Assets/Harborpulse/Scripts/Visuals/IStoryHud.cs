// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using DeviceChain.Harborpulse.Model.Director;

namespace DeviceChain.Harborpulse.Visuals
{
    /// <summary>ugui is supplied by the Hud assembly; portable visuals need only this interface.</summary>
    public interface IStoryHud : IDisposable
    {
        bool Visible { get; set; }
        void Apply(DirectorView view, string badge, double sim, string rate, IReadOnlyList<WorldTag> tags, string extraProblem);
    }
}
