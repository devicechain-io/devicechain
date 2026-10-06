// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Shows the intro's image (premultiplied alpha) on the overlay canvas: where the image is clear
// the demo shows through, and the image colour's alpha (the overlay's cover) fades all of it.
Shader "Hidden/DeviceChain/Intro/Overlay"
{
    Properties
    {
        [PerRendererData] _MainTex ("Image", 2D) = "black" {}
    }
    SubShader
    {
        Tags { "Queue" = "Transparent" "IgnoreProjector" = "True" "RenderType" = "Transparent" "PreviewType" = "Plane" }
        Cull Off
        Lighting Off
        ZWrite Off
        ZTest [unity_GUIZTestMode]
        Blend One OneMinusSrcAlpha

        Pass
        {
            CGPROGRAM
            #pragma vertex vert
            #pragma fragment frag
            #include "UnityCG.cginc"

            sampler2D _MainTex;
            struct appdata { float4 vertex : POSITION; float4 color : COLOR; float2 uv : TEXCOORD0; };
            struct v2f { float4 pos : SV_POSITION; float4 color : COLOR; float2 uv : TEXCOORD0; };

            v2f vert(appdata v)
            {
                v2f o;
                o.pos = UnityObjectToClipPos(v.vertex);
                o.color = v.color;
                o.uv = v.uv;
                return o;
            }

            fixed4 frag(v2f i) : SV_Target
            {
                return tex2D(_MainTex, i.uv) * i.color.a;
            }
            ENDCG
        }
    }
}
