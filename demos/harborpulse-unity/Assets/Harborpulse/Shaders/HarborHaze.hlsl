// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The haze a view ray fades into, shared by the sky (its horizon band and everything under the horizon), the scenery's fog and the open
// water's, so the three never disagree along an edge. It is the horizon's colour at and above the horizon, darkening to the far ground's
// below it (DayLighting.HazeColor), and in the rain it carries shafts: slanted bands of heavier rain that hang between the eye and the
// far land or sea, so a storm's distance is never one flat grey field (SkyState.RainShaftDepth).
#ifndef HARBOR_HAZE_INCLUDED
#define HARBOR_HAZE_INCLUDED

float4 _HarborCloud;       // x cover, y depth, z rain (HarborSkyController)
float4 _HarborCloudDrift;  // xy the low cloud layer's offset, zw the high layer's

float HarborHazeHash(float2 p)
{
    p = frac(p * float2(123.34, 456.21));
    p += dot(p, p + 45.32);
    return frac(p.x * p.y);
}

float HarborHazeNoise(float2 p)
{
    float2 i = floor(p);
    float2 f = frac(p);
    float2 u = f * f * (3.0 - 2.0 * f);
    return lerp(lerp(HarborHazeHash(i), HarborHazeHash(i + float2(1, 0)), u.x), lerp(HarborHazeHash(i + float2(0, 1)), HarborHazeHash(i + float2(1, 1)), u.x), u.y);
}

// 0 .. 1: how much of a rain shaft a unit view ray passes through. Sampled round the compass on a circle, so no bearing has a seam, and
// slanted by the ray's elevation, so a shaft leans with the wind. It moves with the cloud, by the scenario clock.
float HarborRainShaft(float3 d)
{
    float2 az = normalize(d.xz + float2(1e-5, 0.0));
    float2 around = az * 4.5 + float2(d.y * 4.0 + _HarborCloudDrift.x * 0.4, 2.7);
    return smoothstep(0.42, 0.78, HarborHazeNoise(around)) * _HarborCloud.z;
}

// 0 .. 1: the squalls inside the rain, seen from above. A ray that looks down on far land or sea crosses the rain at a slant, so the
// squalls show as long streaks leaning with the wind and as bands lying along the distance: narrow ones (a twelfth of the compass), and
// between them gaps the far light comes through. Drawn only below the horizon: above it the sky has its own curtains.
float HarborSquall(float3 d)
{
    float2 az = normalize(d.xz + float2(1e-5, 0.0));
    float down = max(-d.y, 0.0);
    // streaks: fine round the compass, long down the frame, leaning
    float streak = HarborHazeNoise(az * 21.0 + float2(down * 13.0 + _HarborCloudDrift.x * 0.7, 7.1 - down * 7.0));
    // bands: the far rain lies in ranks, one behind another, broken and bowed round the compass
    float band = HarborHazeNoise(float2(down * 27.0 + az.x * 4.5, az.y * 6.0 + 1.9 + _HarborCloudDrift.x * 0.25));
    return (smoothstep(0.36, 0.78, streak) * 0.75 + smoothstep(0.50, 0.80, band) * 0.4) * _HarborCloud.z;
}

// The haze colour along a unit view ray.
float3 HarborHazeColor(float3 d, float3 horizonHaze, float3 groundHaze)
{
    float3 c = lerp(horizonHaze, groundHaze, 1.0 - smoothstep(-0.05, 0.004, d.y));
    // a real branch on a uniform: a dry day pays nothing
    [branch] if (_HarborCloud.z > 0.001)
    {
        // the shafts thin out towards the ground under the eye: they are a far thing
        float far = 1.0 - smoothstep(0.0, 0.34, -d.y);
        float shaft = HarborRainShaft(d) * far;
        c *= 1.0 - 0.42 * shaft;
        // Seen from high up (the 9:16 HOME looks east along the coast from 230 m), a storm's distance was one grey field from the
        // horizon to the near shore. Squalls darken it in streaks and ranks, and between them the haze is a little paler than the mean:
        // the far light under the deck. They start just under the horizon, so the horizon's own line stays the fog's colour, and are
        // at their strongest between 2 and 12 degrees down, where the far land and sea are.
        float under = smoothstep(0.004, 0.035, -d.y) * (1.0 - smoothstep(0.20, 0.36, -d.y));
        float squall = HarborSquall(d);
        c *= 1.0 + under * _HarborCloud.z * (0.12 - 0.34 * squall);
    }
    return c;
}

#endif
