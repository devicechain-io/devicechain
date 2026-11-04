---
sidebar_position: 4.8
title: Connecting devices from outside the cluster
---

# Connecting devices from outside the cluster {#connecting-from-outside}

Devices reach the platform's MQTT broker over TLS on TCP **8883**. On a cloud cluster nothing is open to the internet until you ask for it: a new install and every upgrade leave MQTT inside the cluster, and you switch exposure on per instance with `dcctl`. This page covers the two ways to do that, the DNS name and certificate your devices need, the firewall, and how to check the result from a machine outside the cluster.

On a local `kind` cluster none of this is needed: `dcctl` already maps port 1883 on your machine to the broker, as described in [Connecting a device](../guides/connecting-a-device.md#publishing-an-event).

:::note Plaintext MQTT is never exposed
Both modes carry only the broker's TLS listener. The plaintext path is not published, and `dcctl` refuses to expose MQTT on an instance that serves no TLS (`--no-tls`). The broker itself terminates TLS and does the device authentication, including [device certificates](./device-certificates.md); the ingress controller never sees the traffic in the clear.
:::

## Choose a mode {#modes}

| | `gateway` | `loadbalancer` |
| --- | --- | --- |
| What is created | A route on the instance's Gateway, served by Traefik on TCP 8883 | A `Service` of type `LoadBalancer` for TCP 8883 straight to the broker pods |
| How a device finds its instance | By the host name it connects to (TLS server name indication, SNI) | By the balancer's own address; the host name is only DNS |
| Instances per address | Many: one shared address, one host name each | One balancer per instance |
| What the broker sees as the device's address | The ingress controller's | The device's own, where the cloud balancer passes it through |
| Needs | The Gateway front end and Gateway API (the default on a new install), and a one-time cluster step | A cloud that provisions `LoadBalancer` Services |

Use `gateway` where the instance is already served through Traefik and you want one address for several instances. Use `loadbalancer` on a cluster without Gateway API support, or when you want an L4 balancer of your own for the broker, for example to restrict it to a set of source ranges.

## Turn it on {#enable}

Pick a **host name** for the instance, for example `mqtt.acme.example.com`. Devices dial this name, the broker's certificate carries it, and in `gateway` mode the Gateway routes by it. It must be a fully-qualified, lower-case DNS name, and every instance on a cluster needs its own.

### Gateway mode {#enable-gateway}

Gateway mode needs Traefik to listen on 8883. That publishes a new port on its load balancer, so it is a cluster step you take once, deliberately:

```bash
dcctl install <provider> --mqtt-gateway
```

Give `install` the same flags the cluster was installed with, such as `--ha`, as for any [rolling upgrade](./releases-and-upgrades.md#rolling-upgrades). Without `--mqtt-gateway` an install keeps whatever the controller has, so a later install never closes the port by accident; `--mqtt-gateway=false` closes it.

Then, per instance:

```bash
dcctl bootstrap <provider> acme --mqtt-exposure gateway --mqtt-host mqtt.acme.example.com
# or, on an instance that already exists:
dcctl upgrade   <provider> acme --mqtt-exposure gateway --mqtt-host mqtt.acme.example.com
```

`dcctl` refuses the request, before it changes anything, when Traefik is not listening on 8883, when the instance is not served by the Gateway front end, or when another instance already uses the host name.

### Load balancer mode {#enable-loadbalancer}

```bash
dcctl bootstrap <provider> acme --mqtt-exposure loadbalancer --mqtt-host mqtt.acme.example.com
dcctl upgrade   <provider> acme --mqtt-exposure loadbalancer --mqtt-host mqtt.acme.example.com
```

This creates the `dc-nats-mqtt-lb` Service in the instance's namespace, with `externalTrafficPolicy: Local`. Cloud-specific annotations, such as an internal balancer, and a list of allowed source ranges are set through the [tuning overlay](./tuning-an-instance.md) under `mqttExposure.loadBalancer`:

```yaml
mqttExposure:
  loadBalancer:
    annotations:
      service.beta.kubernetes.io/aws-load-balancer-scheme: internal
    sourceRanges:
      - 203.0.113.0/24
```

On EKS, a `LoadBalancer` Service gets a Classic balancer unless it is annotated for a Network Load Balancer; add `service.beta.kubernetes.io/aws-load-balancer-type: nlb` there. For Traefik's own balancer in gateway mode, `dcctl install --aws-nlb traefik` does it.

### What an upgrade does {#enable-effects}

The setting is recorded on the instance and kept by every later `dcctl upgrade`, so you give the flags once. The host name is added to the broker's certificate, and a broker that does not yet carry it gets a new certificate and **restarts**. A broker restart is a short interruption for connected devices and, as with any broker roll, a window in which acknowledged messages can be lost unless [fail-closed MQTT](../reference/transport-matrix.md#mqtt--the-platform-broker) is on. Plan it as you would a version upgrade.

Turn exposure off again with `--mqtt-exposure none`. It removes the route or the Service; the certificate keeps the extra name until its next renewal, which is harmless.

## DNS {#dns}

Point the host name at the address that now answers on 8883:

```bash
# gateway: the ingress controller's address
kubectl -n traefik get service traefik
# loadbalancer: the instance's broker balancer
kubectl -n dci-acme get service dc-nats-mqtt-lb
```

Use an `A` record for an IP address and a `CNAME` for a name, as the cloud reports it (EKS reports a name, GKE and AKS an IP address). The host name can be the same one the instance serves its console on, or a separate one such as `mqtt.acme.example.com`. In gateway mode several instances share the controller's address, each with its own name.

## Ports and the firewall {#firewall}

| Port | Who connects | Open to |
| --- | --- | --- |
| TCP 8883 | Devices, to the controller's or the broker balancer's address | The networks your devices are on |

Nothing else needs opening for MQTT. The cloud creates the rule for a `LoadBalancer` Service on GKE and AKS. On AWS, check that the security group of the balancer, and of the nodes behind it, allows TCP 8883 from your devices. Cloud balancers that pass the connection through at layer 4 are required: a layer 7 HTTP balancer cannot carry MQTT.

In gateway mode the broker sees the ingress controller's address as the source of every device, not the device's own. Anything you key on a device's source address at the broker is therefore shared by all the devices behind one controller. Load balancer mode with a balancer that preserves the client address (a Network Load Balancer on AWS, Google Cloud's network load balancer) does not have this limit.

## The certificate your devices must trust {#certificates}

The broker presents a certificate issued by the instance's own certificate authority, and the host name you chose is among its names. Devices must trust that authority, as for any [MQTT connection to the instance](../guides/connecting-a-device.md#publishing-an-event). Export it from a machine with access to the cluster:

```bash
kubectl -n dci-acme get secret dc-nats-tls -o jsonpath='{.data.ca\.crt}' | base64 -d > instance-ca.crt
```

Ship `instance-ca.crt` with your device firmware or configuration. It is a public certificate. The broker's certificate is renewed by `dcctl upgrade` when it is within 30 days of expiry; the authority stays the same, so devices keep trusting it across renewals.

Devices must send the host name as the TLS server name. In gateway mode this is how the right instance is found: a connection that sends no server name, or one that the Gateway has no route for, is closed. Connecting by IP address therefore fails in gateway mode; connect by name.

## Check it from outside {#verify}

From a machine outside the cluster, once DNS resolves:

```bash
# 1. The right certificate answers, and it verifies against the instance CA
openssl s_client -connect mqtt.acme.example.com:8883 \
  -servername mqtt.acme.example.com -CAfile instance-ca.crt </dev/null \
  | grep -E 'subject=|Verify return code'
```

You should see the host name in the subject (or among the alternative names) and `Verify return code: 0 (ok)`.

```bash
# 2. A device can connect and publish
mosquitto_pub --cafile instance-ca.crt \
  -h mqtt.acme.example.com -p 8883 \
  -i 'devicechain:acme:sensor-001' \
  -u 'acme:<credentialId>' -P '<credentialSecret>' \
  -t 'devicechain/acme/devices/sensor-001/events' \
  -m '{"device":"sensor-001","eventType":"Measurement","payload":{"entries":[{"measurements":{"temperature":"21.5"}}]}}'
```

Add `-d` to see the broker's `CONNACK` and the `PUBLISH`. A device that authenticates with a certificate adds `--cert` and `--key`, as in [Connecting with a certificate](../guides/device-x509.md).

## When it does not work {#troubleshooting}

| What you see | What it means |
| --- | --- |
| `dcctl` refuses with "needs the ingress controller to listen on 8883" | Run `dcctl install <provider> --mqtt-gateway` once for the cluster, or use `--mqtt-exposure loadbalancer` |
| `dcctl` refuses because TLS is off | The instance serves plain HTTP (`--no-tls`). Plaintext MQTT is never exposed; serve the instance over TLS |
| `dcctl` refuses because the host is taken | Another instance on the cluster uses that name. Give each instance its own |
| The connection is closed during the handshake, `no peer certificate available` | In gateway mode: no server name was sent, or no route matches it. Connect by the exact host name; check `kubectl -n dci-acme get tlsroute devicechain-mqtt` |
| `certificate verify failed` or a host name mismatch | The device trusts a different authority, or dials a name the certificate does not carry. Check the name against the output of step 1 above |
| Times out | DNS points elsewhere, or the firewall or security group blocks TCP 8883. In gateway mode check that `kubectl -n traefik get service traefik` lists 8883 |
| The Gateway's `mqtt-tls` listener is not `Programmed` | `kubectl -n dci-acme describe gateway devicechain` says why; the usual cause is no 8883 entrypoint on the controller |

The platform's MQTT behaviour, ingest limits and delivery guarantees are the same however a device reaches the broker; see [the MQTT transport](../reference/transport-matrix.md#mqtt--the-platform-broker).
