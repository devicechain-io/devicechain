# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

"""A Sparkplug B edge node built on the Eclipse Tahu Python library.

One node ("pump-station") with one device ("pump-1"). It is modelled on Tahu's own
python/examples/example.py, reduced to what this example needs:

  NBIRTH / DBIRTH   the node and the pump declare their metrics
  DDATA             temperature and pressure every PUBLISH_SECONDS
  NCMD              "Node Control/Rebirth" republishes the births
  DCMD              a write to an output: apply it, then REPORT it in a DDATA

The report is the point. Sparkplug has no command acknowledgement, so the host decides a write
worked only because the node later publishes the metric at the new value.

Outputs/Interlock is deliberately a write the node never applies and never reports, so the
example has a command that ends unconfirmed.
"""

import os
import random
import sys
import threading
import time

import paho.mqtt.client as mqtt

# Tahu's own library, copied into /tahu/core at image build time (see the Dockerfile).
sys.path.insert(0, "/tahu/core")
import sparkplug_b as sparkplug  # noqa: E402
import sparkplug_b_pb2  # noqa: E402
from sparkplug_b import MetricDataType, addMetric  # noqa: E402

BROKER_HOST = os.environ.get("BROKER_HOST", "mosquitto")
BROKER_PORT = int(os.environ.get("BROKER_PORT", "1883"))
USERNAME = os.environ["BROKER_USERNAME"]
PASSWORD = os.environ["BROKER_PASSWORD"]
GROUP = os.environ.get("SPB_GROUP", "plant-a")
NODE = os.environ.get("SPB_NODE", "pump-station")
DEVICE = os.environ.get("SPB_DEVICE", "pump-1")
PUBLISH_SECONDS = float(os.environ.get("PUBLISH_SECONDS", "5"))
# Set STALE_TIMESTAMPS=1 to publish exactly what Tahu's helper does by default (see metric()).
STALE_TIMESTAMPS = os.environ.get("STALE_TIMESTAMPS", "") not in ("", "0")


class Alias:
    """Aliases are numbers a BIRTH assigns to names. Later messages (and commands) may use either."""

    REBIRTH = 1
    TEMPERATURE = 10
    PRESSURE = 11
    RUN = 12
    SETPOINT = 13
    INTERLOCK = 14


# The pump's current state. Writes land here; DDATA publishes it.
state = {"run": False, "setpoint": 60.0, "interlock": False}
lock = threading.Lock()


def now_ms():
    return int(round(time.time() * 1000))


def metric(payload, name, alias, datatype, value):
    # 🔴 Tahu quirk: addMetric()'s default `timestamp` is evaluated ONCE, when sparkplug_b is
    # imported, so every metric that omits it carries the process start time. A host that
    # compares a report's timestamp with a command's publish time then sees a report that is
    # older than the command and cannot count it. Always pass the time explicitly.
    if STALE_TIMESTAMPS:
        return addMetric(payload, name, alias, datatype, value)
    return addMetric(payload, name, alias, datatype, value, timestamp=now_ms())


def topic(kind, device=None):
    base = f"spBv1.0/{GROUP}/{kind}/{NODE}"
    return base if device is None else f"{base}/{device}"


def publish_births(client):
    # The node's own metrics. getNodeBirthPayload() adds bdSeq and resets seq to 0.
    nbirth = sparkplug.getNodeBirthPayload()
    metric(nbirth, "Node Control/Rebirth", Alias.REBIRTH, MetricDataType.Boolean, False)
    client.publish(topic("NBIRTH"), bytearray(nbirth.SerializeToString()), 0, False)

    # The device's metrics, each with an alias, declared once and referenced by alias after that.
    with lock:
        s = dict(state)
    dbirth = sparkplug.getDeviceBirthPayload()
    metric(dbirth, "Temperature", Alias.TEMPERATURE, MetricDataType.Double, temperature(s))
    metric(dbirth, "Pressure", Alias.PRESSURE, MetricDataType.Double, pressure(s))
    metric(dbirth, "Outputs/Run", Alias.RUN, MetricDataType.Boolean, s["run"])
    metric(dbirth, "Outputs/Setpoint", Alias.SETPOINT, MetricDataType.Double, s["setpoint"])
    metric(dbirth, "Outputs/Interlock", Alias.INTERLOCK, MetricDataType.Boolean, s["interlock"])
    client.publish(topic("DBIRTH", DEVICE), bytearray(dbirth.SerializeToString()), 0, False)
    print(f"published NBIRTH and DBIRTH for {GROUP}/{NODE}/{DEVICE}", flush=True)


def temperature(s):
    return round((75.0 if s["run"] else 21.0) + random.uniform(-0.5, 0.5), 2)


def pressure(s):
    return round((4.2 if s["run"] else 1.0) + random.uniform(-0.05, 0.05), 3)


def publish_ddata(client, items):
    """items: (alias, datatype, value). No names: a DDATA refers to what the DBIRTH declared."""
    payload = sparkplug.getDdataPayload()
    for alias, datatype, value in items:
        metric(payload, None, alias, datatype, value)
    client.publish(topic("DDATA", DEVICE), bytearray(payload.SerializeToString()), 0, False)


def on_connect(client, userdata, flags, rc):
    if rc != 0:
        print(f"connect refused, rc={rc}", flush=True)
        sys.exit(1)
    client.subscribe(topic("NCMD"))
    client.subscribe(f"spBv1.0/{GROUP}/DCMD/{NODE}/#")
    publish_births(client)


def is_metric(m, name, alias):
    return m.name == name or (m.alias == alias and m.name == "")


def on_message(client, userdata, msg):
    payload = sparkplug_b_pb2.Payload()
    payload.ParseFromString(msg.payload)
    kind = msg.topic.split("/")[2]
    for m in payload.metrics:
        if is_metric(m, "Node Control/Rebirth", Alias.REBIRTH):
            print("NCMD Node Control/Rebirth: publishing births again", flush=True)
            publish_births(client)
        elif kind == "DCMD" and is_metric(m, "Outputs/Run", Alias.RUN):
            apply_and_report(client, "run", m.boolean_value, Alias.RUN, MetricDataType.Boolean)
        elif kind == "DCMD" and is_metric(m, "Outputs/Setpoint", Alias.SETPOINT):
            apply_and_report(client, "setpoint", m.double_value, Alias.SETPOINT, MetricDataType.Double)
        elif kind == "DCMD" and is_metric(m, "Outputs/Interlock", Alias.INTERLOCK):
            # Accepted off the wire, never applied, never reported.
            print("DCMD Outputs/Interlock ignored: the interlock is hardwired", flush=True)
        else:
            print(f"ignored {kind} metric name={m.name!r} alias={m.alias}", flush=True)


def apply_and_report(client, key, value, alias, datatype):
    # A real node would drive the output, read it back, and publish what it read.
    with lock:
        state[key] = value
    print(f"DCMD {key} = {value}: applied, reporting it", flush=True)
    publish_ddata(client, [(alias, datatype, value)])


def main():
    # 🔴 Tahu quirk: bdSeq starts at 0 in every process. A restarted node therefore reuses the
    # bdSeq of the session that just died, which a host cannot tell from the same session.
    # Start somewhere else on each run (the spec only asks that it change per connection).
    sparkplug.bdSeq = random.randrange(256)

    client = mqtt.Client(client_id=f"{NODE}-{random.randrange(1 << 20)}")
    client.username_pw_set(USERNAME, PASSWORD)

    # The will is the NDEATH the broker publishes if this connection drops. Its bdSeq must be
    # the one the NBIRTH carries, so build it BEFORE the birth (getNodeDeathPayload() advances
    # bdSeq and getNodeBirthPayload() reports the value before it).
    death = sparkplug.getNodeDeathPayload()
    client.will_set(topic("NDEATH"), bytearray(death.SerializeToString()), 0, False)

    client.on_connect = on_connect
    client.on_message = on_message
    client.connect(BROKER_HOST, BROKER_PORT, 60)
    client.loop_start()

    while True:
        time.sleep(PUBLISH_SECONDS)
        with lock:
            s = dict(state)
        publish_ddata(
            client,
            [
                (Alias.TEMPERATURE, MetricDataType.Double, temperature(s)),
                (Alias.PRESSURE, MetricDataType.Double, pressure(s)),
            ],
        )


if __name__ == "__main__":
    main()
