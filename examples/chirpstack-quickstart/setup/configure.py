# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""Configures ChirpStack for the example over its gRPC API, signing in as the stock admin user.

It creates, if they do not exist already: a gateway, a device profile with a JavaScript payload
codec, an application, the application's HTTP integration (the part that talks to DeviceChain),
and two ABP devices. Running it again leaves what exists alone and re-points the HTTP integration.
"""
import os
import sys
import time

import grpc
from chirpstack_api import api, common

SERVER = os.environ.get("CHIRPSTACK", "chirpstack:8080")
UPLINK_URL = os.environ["DC_UPLINK_URL"]
with open("/run/integration-token") as f:
    BEARER = f.read().strip()

# The same keys the simulator signs with.
NWK_S_KEY = "2b7e151628aed2a6abf7158809cf4f3c"
APP_S_KEY = "3c4fcf098815f7aba6d2ae2816157e2b"
GATEWAY_ID = "0102030405060708"
DEVICES = [
    ("70b3d57ed0012345", "01020301", "Boiler room 1"),
    ("70b3d57ed0067890", "01020302", "Cold store (not yet in DeviceChain)"),
]

# Decodes the simulator's four-byte payload: temperature (hundredths, int16), humidity (%), battery (tenths of a volt).
CODEC = """
function decodeUplink(input) {
  var b = input.bytes;
  var t = (b[0] << 8) | b[1];
  if (t & 0x8000) { t -= 0x10000; }
  return { data: { temperature: t / 100, humidity: b[2], battery: b[3] / 10 } };
}
"""


def connect():
    chan = grpc.insecure_channel(SERVER)
    deadline = time.time() + 120
    while True:
        try:
            grpc.channel_ready_future(chan).result(timeout=5)
            resp = api.InternalServiceStub(chan).Login(api.LoginRequest(email="admin", password="admin"))
            return chan, [("authorization", "Bearer " + resp.jwt)]
        except Exception as e:  # ChirpStack is still starting
            if time.time() > deadline:
                sys.exit(f"ChirpStack at {SERVER} did not come up: {e}")
            time.sleep(3)


def main():
    chan, md = connect()
    tenants = api.TenantServiceStub(chan).List(api.ListTenantsRequest(limit=10), metadata=md)
    tenant_id = tenants.result[0].id

    gws = api.GatewayServiceStub(chan)
    try:
        gws.Get(api.GetGatewayRequest(gateway_id=GATEWAY_ID), metadata=md)
        print("gateway exists")
    except grpc.RpcError:
        gws.Create(api.CreateGatewayRequest(gateway=api.Gateway(
            gateway_id=GATEWAY_ID, name="Simulated gateway", tenant_id=tenant_id, stats_interval=30)), metadata=md)
        print("created gateway", GATEWAY_ID)

    dps = api.DeviceProfileServiceStub(chan)
    found = [p for p in dps.List(api.ListDeviceProfilesRequest(tenant_id=tenant_id, limit=100), metadata=md).result
             if p.name == "Example sensor"]
    if found:
        profile_id = found[0].id
        print("device profile exists")
    else:
        profile_id = dps.Create(api.CreateDeviceProfileRequest(device_profile=api.DeviceProfile(
            name="Example sensor", tenant_id=tenant_id, region=common.Region.EU868,
            mac_version=common.MacVersion.LORAWAN_1_0_3, reg_params_revision=common.RegParamsRevision.A,
            supports_otaa=False, uplink_interval=60, adr_algorithm_id="default", flush_queue_on_activate=True,
            payload_codec_runtime=api.CodecRuntime.JS, payload_codec_script=CODEC)), metadata=md).id
        print("created device profile with a JavaScript codec")

    apps = api.ApplicationServiceStub(chan)
    found = [a for a in apps.List(api.ListApplicationsRequest(tenant_id=tenant_id, limit=100), metadata=md).result
             if a.name == "Example app"]
    if found:
        app_id = found[0].id
        print("application exists")
    else:
        app_id = apps.Create(api.CreateApplicationRequest(application=api.Application(
            name="Example app", tenant_id=tenant_id)), metadata=md).id
        print("created application")

    # The HTTP integration: where ChirpStack posts, and the bearer token DeviceChain issued.
    # The instance routes by host name, and the stack reaches it through the Docker host, so the
    # name it expects goes in a Host header.
    headers = {"Authorization": "Bearer " + BEARER}
    if os.environ.get("DC_INGRESS_HOST"):
        headers["Host"] = os.environ["DC_INGRESS_HOST"]
    integration = api.HttpIntegration(
        application_id=app_id, headers=headers,
        encoding=api.Encoding.JSON, event_endpoint_url=UPLINK_URL)
    try:
        apps.GetHttpIntegration(api.GetHttpIntegrationRequest(application_id=app_id), metadata=md)
        apps.UpdateHttpIntegration(api.UpdateHttpIntegrationRequest(integration=integration), metadata=md)
        print("updated the HTTP integration ->", UPLINK_URL)
    except grpc.RpcError:
        apps.CreateHttpIntegration(api.CreateHttpIntegrationRequest(integration=integration), metadata=md)
        print("created the HTTP integration ->", UPLINK_URL)

    devs = api.DeviceServiceStub(chan)
    for eui, addr, name in DEVICES:
        try:
            devs.Get(api.GetDeviceRequest(dev_eui=eui), metadata=md)
            print("device exists", eui)
            continue
        except grpc.RpcError:
            pass
        devs.Create(api.CreateDeviceRequest(device=api.Device(
            dev_eui=eui, name=name, application_id=app_id, device_profile_id=profile_id,
            skip_fcnt_check=True)), metadata=md)
        devs.Activate(api.ActivateDeviceRequest(device_activation=api.DeviceActivation(
            dev_eui=eui, dev_addr=addr, app_s_key=APP_S_KEY, nwk_s_enc_key=NWK_S_KEY,
            s_nwk_s_int_key=NWK_S_KEY, f_nwk_s_int_key=NWK_S_KEY)), metadata=md)
        print("created and activated device", eui)


main()
