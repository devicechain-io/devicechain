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
// Quay rectangles: xmin, zmin, xmax, zmax, in the same order as _TideLevels.
float4 _TideRects[3];
float _WaterTime;

// Identical to Model/TideField. d=0 uses the exact inverse-distance limit.
float HarborTide(float2 xz)
{
    float sum = 0, weights = 0;
    [unroll] for (int q = 0; q < 3; q++)
    {
        float2 d = max(max(_TideRects[q].xy - xz, 0), xz - _TideRects[q].zw);
        float d2 = dot(d, d);
        if (d2 == 0) return _TideLevels[q];
        float w = 1.0 / (d2 + 0.0001);
        sum += w * _TideLevels[q]; weights += w;
    }
    return sum / weights;
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

// Shared lighting for the harbour and the reflection-free wet wells.
float3 HarborWaterLight(float3 body, float3 normal, float3 view, float3 lightDir, float3 lightColour, float3 ambient)
{
    float diffuse = saturate(dot(normal, lightDir));
    float spec = pow(saturate(dot(reflect(-view, normal), lightDir)), 90.0);
    return body * (ambient + lightColour * diffuse * 0.4) + lightColour * spec * 0.4;
}

#endif
