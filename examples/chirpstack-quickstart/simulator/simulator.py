# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""A LoRaWAN gateway and two ABP sensors, in one process.

It builds real LoRaWAN 1.0.x unconfirmed uplinks (encrypted payload, message integrity code) and
hands them to ChirpStack Gateway Bridge in the Semtech UDP packet-forwarder format, exactly as a
physical gateway would. ChirpStack therefore sees genuine radio frames, verifies them, decodes
them with the device profile's JavaScript codec and forwards the decoded uplink to DeviceChain.

The payload is four bytes: temperature in hundredths of a degree (int16, big endian), relative
humidity in percent (uint8) and battery in tenths of a volt (uint8).
"""
import base64
import json
import math
import os
import socket
import struct
import time
from datetime import datetime, timezone

from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes
from cryptography.hazmat.primitives.cmac import CMAC

GATEWAY_ID = bytes.fromhex(os.environ.get("GATEWAY_ID", "0102030405060708"))
GATEWAY_HOST = os.environ.get("GATEWAY_HOST", "gateway-bridge")
GATEWAY_PORT = int(os.environ.get("GATEWAY_PORT", "1700"))
INTERVAL = float(os.environ.get("INTERVAL_SECONDS", "10"))

# These keys and addresses are the example's own; ./connect-chirpstack.sh gives ChirpStack the same.
NWK_S_KEY = bytes.fromhex("2b7e151628aed2a6abf7158809cf4f3c")
APP_S_KEY = bytes.fromhex("3c4fcf098815f7aba6d2ae2816157e2b")
DEVICES = [
    # (DevEUI, DevAddr, phase, offset): the first is registered in DeviceChain, the second is not.
    ("70b3d57ed0012345", 0x01020301, 0.0, 21.0),
    ("70b3d57ed0067890", 0x01020302, 2.0, 18.0),
]


def aes_ecb(key: bytes, block: bytes) -> bytes:
    enc = Cipher(algorithms.AES(key), modes.ECB()).encryptor()
    return enc.update(block) + enc.finalize()


def crypt_payload(app_s_key: bytes, dev_addr: int, fcnt: int, payload: bytes) -> bytes:
    """LoRaWAN FRMPayload encryption, uplink direction."""
    out = bytearray()
    for i in range((len(payload) + 15) // 16):
        a = b"\x01" + b"\x00" * 4 + b"\x00" + struct.pack("<I", dev_addr) + struct.pack("<I", fcnt) + b"\x00" + bytes([i + 1])
        s = aes_ecb(app_s_key, a)
        chunk = payload[i * 16:(i + 1) * 16]
        out += bytes(x ^ y for x, y in zip(chunk, s))
    return bytes(out)


def mic(nwk_s_key: bytes, dev_addr: int, fcnt: int, msg: bytes) -> bytes:
    b0 = b"\x49" + b"\x00" * 4 + b"\x00" + struct.pack("<I", dev_addr) + struct.pack("<I", fcnt) + b"\x00" + bytes([len(msg)])
    c = CMAC(algorithms.AES(nwk_s_key))
    c.update(b0 + msg)
    return c.finalize()[:4]


def uplink(dev_addr: int, fcnt: int, payload: bytes) -> bytes:
    mhdr = b"\x40"  # unconfirmed data up
    fhdr = struct.pack("<I", dev_addr) + b"\x00" + struct.pack("<H", fcnt & 0xFFFF)
    msg = mhdr + fhdr + b"\x01" + crypt_payload(APP_S_KEY, dev_addr, fcnt, payload)
    return msg + mic(NWK_S_KEY, dev_addr, fcnt, msg)


def push_data(phy: bytes, token: int) -> bytes:
    rxpk = {
        "time": datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%fZ"),
        "tmst": int(time.monotonic() * 1e6) & 0xFFFFFFFF,
        "freq": 868.1, "chan": 0, "rfch": 0, "stat": 1, "modu": "LORA",
        "datr": "SF7BW125", "codr": "4/5", "lsnr": 7.5, "rssi": -57,
        "size": len(phy), "data": base64.b64encode(phy).decode(),
    }
    return bytes([2]) + struct.pack(">H", token) + b"\x00" + GATEWAY_ID + json.dumps({"rxpk": [rxpk]}).encode()


def main() -> None:
    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    fcnt = 0
    token = 0
    print(f"gateway {GATEWAY_ID.hex()} -> {GATEWAY_HOST}:{GATEWAY_PORT}, every {INTERVAL:g}s", flush=True)
    while True:
        for eui, addr, phase, offset in DEVICES:
            t = offset + 3.0 * math.sin(fcnt / 6.0 + phase)
            humidity = 40 + fcnt % 20
            payload = struct.pack(">hBB", round(t * 100), humidity, 33)
            sock.sendto(push_data(uplink(addr, fcnt, payload), token), (GATEWAY_HOST, GATEWAY_PORT))
            token = (token + 1) & 0xFFFF
            print(f"uplink dev_eui={eui} fcnt={fcnt} temperature={t:.2f} humidity={humidity} battery=3.3", flush=True)
        fcnt += 1
        time.sleep(INTERVAL)


if __name__ == "__main__":
    main()
