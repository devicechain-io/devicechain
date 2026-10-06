// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The scene's sky, drawn as code to match the stylized diorama: a soft three-stop gradient from
// a haze at the horizon (the scene's fog colour, read live, so the terrain's edge melts into it)
// to a deeper blue overhead, a warm glow round the sun, and a few flat, painted-looking cumulus
// clouds low over the horizon. Each cloud is a row of round puffs on a flat base with a few
// larger puffs on top, shaded by a soft gradient from a cooler base to a lit crown, with a crisp,
// anti-aliased edge. The clouds are laid out in azimuth and elevation, one possible cloud per
// slice of the horizon, so there are only ever a few; _CloudAzimuth turns the whole layer round
// the horizon. Nothing here is a texture.
//
// The edge is anti-aliased with the angle one pixel spans, taken from the view direction's
// screen derivatives, never from the derivative of the cloud's distance or of the azimuth: the
// azimuth wraps round from 1 to 0 behind the viewer and a cloud's distance jumps where one slice
// hands over to the next, and a derivative taken across either is huge, so an edge filtered by it
// draws a one-pixel line down the sky.
Shader "Sitepulse/Stylized Sky"
{
    Properties
    {
        _ZenithColor("Zenith", Color) = (0.30, 0.48, 0.74, 1)
        _MidColor("Mid sky", Color) = (0.50, 0.66, 0.86, 1)
        _SunGlow("Sun glow", Range(0, 2)) = 0.6
        _CloudLit("Cloud, lit", Color) = (0.98, 0.98, 0.97, 1)
        _CloudShade("Cloud, shaded", Color) = (0.74, 0.80, 0.88, 1)
        _CloudCover("Share of horizon slices with a cloud", Range(0, 1)) = 0.6
        _CloudSlices("Horizon slices", Float) = 9
        _CloudSize("Cloud width (share of a slice)", Range(0.1, 0.9)) = 0.42
        _CloudElevation("Cloud base elevation, min and max (degrees)", Vector) = (5, 24, 0, 0)
        _CloudSeed("Seed", Float) = 3.7
        _CloudAzimuth("Cloud layer heading (degrees)", Float) = 0
        [HideInInspector] _CloudMask("Draw only where the clouds are (for framing stills)", Float) = 0
    }

    SubShader
    {
        Tags { "Queue" = "Background" "RenderType" = "Background" "PreviewType" = "Skybox" "RenderPipeline" = "UniversalPipeline" }
        Cull Off
        ZWrite Off

        Pass
        {
            HLSLPROGRAM
            #pragma vertex Vert
            #pragma fragment Frag
            #include "Packages/com.unity.render-pipelines.universal/ShaderLibrary/Core.hlsl"

            CBUFFER_START(UnityPerMaterial)
                half4 _ZenithColor, _MidColor, _CloudLit, _CloudShade;
                half _SunGlow, _CloudCover, _CloudSize;
                float _CloudSlices, _CloudSeed, _CloudAzimuth, _CloudMask;
                float4 _CloudElevation;
            CBUFFER_END

            struct Attributes { float4 positionOS : POSITION; };
            struct Varyings { float4 positionCS : SV_POSITION; float3 dir : TEXCOORD0; };

            Varyings Vert(Attributes v)
            {
                Varyings o;
                o.positionCS = TransformObjectToHClip(v.positionOS.xyz);
                o.dir = v.positionOS.xyz;
                return o;
            }

            float Hash(float n)
            {
                return frac(sin(n * 127.1 + _CloudSeed * 311.7) * 43758.5453);
            }

            // One cloud, in a frame where x is the angle along the horizon from the cloud's centre
            // and y the elevation above its base (radians). Returns the signed distance to its
            // outline (negative inside) and, in lit, how far up its body a point is (0 at the base,
            // 1 at the crown), for a soft shade from the base up.
            float CloudShape(float2 p, float id, float width, out float lit)
            {
                float d = 1e3;
                float top = 0.0;
                // the base row: small puffs along a flat bottom
                const int n = 7;
                for (int k = 0; k < n; k++)
                {
                    float t = (k - (n - 1) * 0.5) / ((n - 1) * 0.5);                // -1..1 along the cloud
                    float r = width * (0.14 - 0.05 * t * t) * (0.8 + 0.4 * Hash(id * 7.0 + k));
                    float2 c = float2(t * width * 0.5, r * 0.55);
                    d = min(d, length(p - c) - r);
                    top = max(top, c.y + r);
                }
                // the crown: two or three larger puffs off-centre, so the outline is never one dome
                for (int m = 0; m < 3; m++)
                {
                    float u = (m - 1.0) * 0.32 + (Hash(id * 3.0 + m + 11.0) - 0.5) * 0.12;
                    float r = width * (m == 1 ? 0.24 : 0.16) * (0.85 + 0.3 * Hash(id * 5.0 + m + 23.0));
                    if (m != 1 && Hash(id + m * 13.0 + 41.0) > 0.75) continue;      // sometimes only two
                    float2 c = float2(u * width, width * 0.1 + r * 0.45);
                    d = min(d, length(p - c) - r);
                    top = max(top, c.y + r);
                }
                d = max(d, -p.y);                                                     // the flat base
                lit = saturate(p.y / max(top, 1e-4));
                return d;
            }

            half4 Frag(Varyings i) : SV_Target
            {
                float3 dir = normalize(i.dir);
                float h = dir.y;

                // the gradient: horizon haze up to the mid sky, then the zenith
                float up = saturate(h);
                half3 horizon = unity_FogColor.rgb;
                half3 col = lerp(horizon, _MidColor.rgb, smoothstep(0.0, 0.22, up));
                col = lerp(col, _ZenithColor.rgb, smoothstep(0.18, 0.85, up));
                col = h < 0.0 ? horizon : col;

                // the sun's glow: wide and soft, in the sun's own colour
                float3 sun = normalize(_MainLightPosition.xyz);
                float cosSun = saturate(dot(dir, sun));
                half3 glowCol = _MainLightColor.rgb / max(max(_MainLightColor.r, _MainLightColor.g), max(_MainLightColor.b, 1e-3));
                col += glowCol * _SunGlow * (pow(cosSun, 8.0) * 0.35 + pow(cosSun, 64.0) * 0.5) * (1.0 - up * 0.5);

                // the clouds: at most one per slice of the horizon, checked in this slice and its
                // neighbours so a cloud may straddle a boundary. One pixel spans this angle (radians):
                // the derivative of the view direction, which is smooth everywhere.
                float pixelAngle = max(length(fwidth(dir)), 1e-5);
                float az = frac(atan2(dir.x, dir.z) / (2.0 * PI) + 0.5 + _CloudAzimuth / 360.0);   // 0..1 round the horizon
                float el = asin(clamp(h, -1.0, 1.0));
                float slice = 2.0 * PI / _CloudSlices;                                // radians per slice
                float s = az * _CloudSlices;
                float cover = 0.0, litAll = 0.0;
                [unroll]
                for (int j = -1; j <= 1; j++)
                {
                    float id = floor(s) + j;
                    float idw = fmod(id + _CloudSlices, _CloudSlices);                // wraps round
                    float base = radians(lerp(_CloudElevation.x, _CloudElevation.y, Hash(idw + 17.0)));
                    float width = slice * _CloudSize * (0.7 + 0.6 * Hash(idw + 31.0)) * lerp(1.0, 0.75, base / radians(30.0));
                    float centre = id + 0.5 + (Hash(idw + 53.0) - 0.5) * 0.3;
                    float2 p = float2((s - centre) * slice * cos(el), el - base);
                    float lit;
                    float d = CloudShape(p, idw, width, lit);
                    float a = saturate(0.5 - d / pixelAngle) * step(Hash(idw), _CloudCover);
                    if (a > cover)
                    {
                        cover = a;
                        litAll = lit;
                    }
                }
                // a soft shade from the base up: cool along the bottom, lit over the crown
                half3 cloud = lerp(_CloudShade.rgb, _CloudLit.rgb, smoothstep(0.08, 0.75, litAll));
                cloud = lerp(cloud, cloud * 0.7 + glowCol * 0.3, pow(cosSun, 4.0) * 0.6);
                cloud = lerp(cloud, horizon, saturate(1.0 - el / radians(4.0)) * 0.6);   // into the haze
                col = lerp(col, cloud, cover * 0.95);
                if (_CloudMask > 0.5) return cover > 0.02 ? half4(1, 0, 1, 1) : half4(0, 1, 0, 1);
                return half4(col, 1.0);
            }
            ENDHLSL
        }
    }
    Fallback Off
}
