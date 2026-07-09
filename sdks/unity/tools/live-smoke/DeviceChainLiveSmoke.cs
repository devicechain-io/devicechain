// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0
//
// Editor live-SDK smoke test (ADR-035 slice 4, VERIFICATION.md item 3): drives the real SDK against a
// live cluster from Unity — login -> selectTenant -> device query (UnityWebRequest HTTP transport) ->
// subscribe to measurementStream (ClientWebSocket transport in the Editor). Logs to the Console.
//
// NOTE: this uses REFLECTION-based System.Text.Json (DefaultJsonTypeInfoResolver), which is fine in
// the Mono Editor but is NOT AOT/IL2CPP-safe — a real player build must supply source-generated
// JsonTypeInfo instead. This script is a dev smoke tool, deliberately kept out of the AOT-safe package.

using System;
using System.Text.Json;
using System.Text.Json.Serialization.Metadata;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Sdk;
using DeviceChain.Sdk.Json;
using DeviceChain.Sdk.Unity;
using UnityEngine;

public sealed class DeviceChainLiveSmoke : MonoBehaviour
{
    [Header("Cluster")]
    public string Origin = "http://localhost";
    public string Email = "superuser@devicechain.local";
    public string Password = "devicechain";
    public string Tenant = "sim-bp";

    [Header("Run")]
    public bool RunOnStart = true;

    private DeviceChainClient _client;
    private CancellationTokenSource _cts;

    // Reflection resolver — EDITOR SMOKE ONLY (see file header). Camel-case matches the GraphQL keys.
    private static readonly JsonSerializerOptions Opts = new JsonSerializerOptions
    {
        PropertyNamingPolicy = JsonNamingPolicy.CamelCase,
        TypeInfoResolver = new DefaultJsonTypeInfoResolver(),
    };

    private static JsonTypeInfo<T> Info<T>() => (JsonTypeInfo<T>)Opts.GetTypeInfo(typeof(T));

    private void Start()
    {
        if (RunOnStart)
        {
            _ = RunAsync();
        }
    }

    [ContextMenu("Run Live Smoke")]
    private void RunFromMenu() => _ = RunAsync();

    private async Task RunAsync()
    {
        _cts?.Cancel();
        _cts = new CancellationTokenSource();
        CancellationToken ct = _cts.Token;

        try
        {
            Debug.Log($"[DC] creating client for {Origin}");
            _client = DeviceChainRuntime.CreateClient(Origin);

            Debug.Log("[DC] login…");
            await _client.LoginAsync(Email, Password, ct);
            Debug.Log($"[DC] selectTenant {Tenant}…");
            await _client.SelectTenantAsync(Tenant, ct);
            Debug.Log("[DC] authenticated ✓");

            // Device query — exercises the UnityWebRequest HTTP transport end-to-end.
            DevicesData devices = await _client.Gql.SendAsync(
                Area.DeviceManagement,
                "{devices(criteria:{pageNumber:1,pageSize:5}){results{token name}}}",
                EmptyVariables.Value, Info<EmptyVariables>(), Info<DevicesData>(), null, ct);
            DeviceRow[] rows = devices?.Devices?.Results ?? Array.Empty<DeviceRow>();
            Debug.Log($"[DC] devices returned: {rows.Length}");
            foreach (DeviceRow d in rows)
            {
                Debug.Log($"[DC]   • {d.Token}");
            }

            // Live subscription — exercises the ClientWebSocket transport (graphql-transport-ws).
            Debug.Log("[DC] subscribing measurementStream… (waiting for live events — run the sim to see data)");
            await foreach (MeasurementStreamData data in _client.Subscriptions(Area.EventManagement).SubscribeAsync(
                "subscription{measurementStream{deviceToken name value unit}}",
                EmptyVariables.Value, Info<EmptyVariables>(), Info<MeasurementStreamData>(), ct))
            {
                MeasurementRow m = data.MeasurementStream;
                Debug.Log($"[DC] 📈 {m.DeviceToken}  {m.Name} = {m.Value} {m.Unit}");
            }
        }
        catch (OperationCanceledException)
        {
            Debug.Log("[DC] stopped");
        }
        catch (Exception ex)
        {
            Debug.LogError("[DC] smoke failed: " + ex);
        }
    }

    private async void OnDestroy()
    {
        _cts?.Cancel();
        if (_client != null)
        {
            try { await _client.DisposeAsync(); } catch { /* teardown */ }
        }
    }

    // ---- response DTOs (camelCase-mapped) ----
    public sealed class DevicesData { public DevicesPage Devices { get; set; } }
    public sealed class DevicesPage { public DeviceRow[] Results { get; set; } }
    public sealed class DeviceRow { public string Token { get; set; } public string Name { get; set; } }

    public sealed class MeasurementStreamData { public MeasurementRow MeasurementStream { get; set; } }
    public sealed class MeasurementRow
    {
        public string DeviceToken { get; set; }
        public string Name { get; set; }
        public double Value { get; set; }
        public string Unit { get; set; }
    }
}
