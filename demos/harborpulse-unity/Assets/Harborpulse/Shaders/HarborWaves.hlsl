// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The harbour surface, shared by the water shader and anything that must sit on it. The numbers are the
// model's (DeviceChain.Harborpulse.Model.GerstnerWaves), handed over by HarborWaterController every frame,
// so this file holds only the evaluation:
//
//   surface height = observed regional tide + zero-mean Gerstner waves
//
// The tide comes from the gauges (HarborReadings.TideBoard), one level per quay, held along each quay's
// wall and blended only between quays. The waves never move the mean.

#ifndef HARBORPULSE_WAVES_INCLUDED
#define HARBORPULSE_WAVES_INCLUDED

// (dir.x, dir.z, steepness Q, wavenumber k) for each of the four components
float4 _WaveA, _WaveB, _WaveC, _WaveD;
// amplitude (m) and angular speed of the four components
float4 _WaveAmp, _WaveOmega;
// the three quays' observed mean levels, west to east (x, y, z)
float4 _TideLevels;
// the quays' wall spans along X, west to east: (min, max) of the first two in _TideSpanAB, of the third in _TideSpanC.xy
float4 _TideSpanAB;
float4 _TideSpanC;
float _WaterTime;

// Along a quay's own wall the level is exactly that quay's (the median its readout shows); only in the open water between two
// quays does it blend to the next. The model's TideProfile.LevelAt is the same mapping, for the CPU side and the tests.
float HarborTide(float x)
{
    if (x <= _TideSpanAB.y) return _TideLevels.x;
    if (x < _TideSpanAB.z) return lerp(_TideLevels.x, _TideLevels.y, smoothstep(_TideSpanAB.y, _TideSpanAB.z, x));
    if (x <= _TideSpanAB.w) return _TideLevels.y;
    if (x < _TideSpanC.x) return lerp(_TideLevels.y, _TideLevels.z, smoothstep(_TideSpanAB.w, _TideSpanC.x, x));
    return _TideLevels.z;
}

void GerstnerOne(float4 wave, float amp, float omega, float2 xz, float t, inout float3 offset, inout float3 tangent, inout float3 binormal)
{
    float2 d = wave.xy;
    float q = wave.z;
    float k = wave.w;
    float f = k * dot(d, xz) - omega * t;
    float c = cos(f);
    float s = sin(f);
    offset += float3(q * amp * d.x * c, amp * s, q * amp * d.y * c);
    tangent += float3(-q * amp * k * d.x * d.x * s, amp * k * d.x * c, -q * amp * k * d.x * d.y * s);
    binormal += float3(-q * amp * k * d.x * d.y * s, amp * k * d.y * c, -q * amp * k * d.y * d.y * s);
}

// The waves at a rest position: the displacement to add, and the surface normal there. ampScale fades the
// waves with distance, where a coarse mesh or a pixel could not show them without aliasing.
void HarborWaves(float2 xz, float ampScale, out float3 offset, out float3 normal)
{
    offset = 0;
    float3 tangent = float3(1, 0, 0);
    float3 binormal = float3(0, 0, 1);
    float4 amp = _WaveAmp * ampScale;
    GerstnerOne(_WaveA, amp.x, _WaveOmega.x, xz, _WaterTime, offset, tangent, binormal);
    GerstnerOne(_WaveB, amp.y, _WaveOmega.y, xz, _WaterTime, offset, tangent, binormal);
    GerstnerOne(_WaveC, amp.z, _WaveOmega.z, xz, _WaterTime, offset, tangent, binormal);
    GerstnerOne(_WaveD, amp.w, _WaveOmega.w, xz, _WaterTime, offset, tangent, binormal);
    normal = normalize(cross(binormal, tangent));
}

#endif
