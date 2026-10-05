// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The intro's own bloom, run on its high-dynamic-range image before the overlay shows it. It
// follows URP's bloom (a soft-knee threshold, a chain of half-size downsamples, upsamples mixed
// by a scatter) so the intro can bloom without turning on post-processing for its camera, which
// would let a demo's colour grading and tonemapping change the lock's brand colours. Passes:
// 0 threshold and downsample, 1 downsample, 2 upsample and mix, 3 composite (the image plus the
// bloom times _Intensity, dithered where the bloom adds light; alpha is the image's).
Shader "Hidden/DeviceChain/Intro/Bloom"
{
    Properties
    {
        _MainTex ("Source", 2D) = "black" {}
    }
    CGINCLUDE
    #include "UnityCG.cginc"

    sampler2D _MainTex, _HighTex, _BloomTex;
    float4 _MainTex_TexelSize;
    float _Threshold, _Scatter, _Intensity;

    struct v2f { float4 pos : SV_POSITION; float2 uv : TEXCOORD0; };

    v2f vert(appdata_img v)
    {
        v2f o;
        o.pos = UnityObjectToClipPos(v.vertex);
        o.uv = v.texcoord;
        return o;
    }

    float3 Box(float2 uv)
    {
        // a 4x4 box filter from four bilinear taps, plus the centre (dual-filter downsample)
        float2 h = _MainTex_TexelSize.xy;
        float3 c = tex2D(_MainTex, uv).rgb * 4.0;
        c += tex2D(_MainTex, uv + float2(-h.x, -h.y)).rgb;
        c += tex2D(_MainTex, uv + float2( h.x, -h.y)).rgb;
        c += tex2D(_MainTex, uv + float2(-h.x,  h.y)).rgb;
        c += tex2D(_MainTex, uv + float2( h.x,  h.y)).rgb;
        return c / 8.0;
    }

    float4 FragPrefilter(v2f i) : SV_Target
    {
        float3 c = min(Box(i.uv), 65000.0);
        float knee = _Threshold * 0.5;
        float br = max(c.r, max(c.g, c.b));
        float rq = clamp(br - _Threshold + knee, 0.0, 2.0 * knee);
        rq = rq * rq / (4.0 * knee + 1e-4);
        c *= max(rq, br - _Threshold) / max(br, 1e-4);
        return float4(c, 1);
    }

    float4 FragDown(v2f i) : SV_Target
    {
        return float4(Box(i.uv), 1);
    }

    float4 FragUp(v2f i) : SV_Target
    {
        // a tent filter over the lower level, mixed into this level by the scatter
        float2 h = _MainTex_TexelSize.xy;
        float3 c = tex2D(_MainTex, i.uv + float2(-h.x * 2.0, 0)).rgb;
        c += tex2D(_MainTex, i.uv + float2(-h.x, h.y)).rgb * 2.0;
        c += tex2D(_MainTex, i.uv + float2(0, h.y * 2.0)).rgb;
        c += tex2D(_MainTex, i.uv + float2(h.x, h.y)).rgb * 2.0;
        c += tex2D(_MainTex, i.uv + float2(h.x * 2.0, 0)).rgb;
        c += tex2D(_MainTex, i.uv + float2(h.x, -h.y)).rgb * 2.0;
        c += tex2D(_MainTex, i.uv + float2(0, -h.y * 2.0)).rgb;
        c += tex2D(_MainTex, i.uv + float2(-h.x, -h.y)).rgb * 2.0;
        c /= 12.0;
        float3 high = tex2D(_HighTex, i.uv).rgb;
        return float4(lerp(high, c, _Scatter), 1);
    }

    float4 FragComposite(v2f i) : SV_Target
    {
        float4 src = tex2D(_MainTex, i.uv);
        float3 bloom = tex2D(_BloomTex, i.uv).rgb * _Intensity;
        float3 c = src.rgb + bloom;
        // the bloom's soft gradients band in the 8-bit output: dither them (and only them, so
        // the mark's colours stay exact) by +-1 step of the encoded value
        float amount = saturate(dot(bloom, float3(0.2126, 0.7152, 0.0722)) * 400.0);
        float n = frac(52.9829189 * frac(dot(i.pos.xy, float2(0.06711056, 0.00583715)))) * 2.0 - 1.0;
        float3 g = pow(max(c, 0.0), 1.0 / 2.2) + n * amount / 255.0;
        c = amount > 0.0 ? pow(max(g, 0.0), 2.2) : c;
        return float4(c, src.a);
    }
    ENDCG

    SubShader
    {
        Cull Off ZWrite Off ZTest Always
        Pass { CGPROGRAM
            #pragma vertex vert
            #pragma fragment FragPrefilter
            ENDCG }
        Pass { CGPROGRAM
            #pragma vertex vert
            #pragma fragment FragDown
            ENDCG }
        Pass { CGPROGRAM
            #pragma vertex vert
            #pragma fragment FragUp
            ENDCG }
        Pass { CGPROGRAM
            #pragma vertex vert
            #pragma fragment FragComposite
            ENDCG }
    }
}
