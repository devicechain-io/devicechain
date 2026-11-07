// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package io.devicechain.conformance;

import java.io.FileInputStream;
import java.io.InputStream;
import java.nio.charset.StandardCharsets;
import java.security.KeyStore;
import java.security.cert.Certificate;
import java.security.cert.CertificateFactory;
import java.util.Arrays;
import java.util.Date;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.Map;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicReference;

import javax.net.ssl.SSLContext;
import javax.net.ssl.SSLSocketFactory;
import javax.net.ssl.TrustManagerFactory;

import org.eclipse.paho.client.mqttv3.IMqttDeliveryToken;
import org.eclipse.paho.client.mqttv3.MqttCallback;
import org.eclipse.paho.client.mqttv3.MqttClient;
import org.eclipse.paho.client.mqttv3.MqttConnectOptions;
import org.eclipse.paho.client.mqttv3.MqttMessage;
import org.eclipse.paho.client.mqttv3.persist.MemoryPersistence;
import org.eclipse.tahu.message.SparkplugBPayloadEncoder;
import org.eclipse.tahu.message.model.Metric.MetricBuilder;
import org.eclipse.tahu.message.model.MetricDataType;
import org.eclipse.tahu.message.model.SparkplugBPayload;
import org.eclipse.tahu.message.model.SparkplugBPayload.SparkplugBPayloadBuilder;

/**
 * The third-party MQTT clients of the transport conformance rig (hack/conformance-rig.sh).
 *
 * <p>Eclipse Paho (MQTT 3.1.1) stands in for a customer's device on the platform broker, and Eclipse
 * Tahu stands in for a Sparkplug B edge node on an operator-owned broker. Neither is code the platform's
 * authors wrote, so a bug the platform shares with its own test client cannot hide here.
 *
 * <p>The client reports what it did on STDOUT as sentinel-prefixed JSON lines (all logging goes to
 * stderr). The rig never judges a check by these lines: it judges by the platform's store and counters,
 * and uses these only to know WHEN to act (a subscription is live) and to explain a failure.
 */
public final class Main {

    static final String SENTINEL = "DCCONF ";

    public static void main(String[] args) throws Exception {
        Map<String, String> a = parseArgs(args);
        String scenario = require(a, "scenario");
        switch (scenario) {
            case "mqtt-publish":
                mqttPublish(a);
                break;
            case "mqtt-command":
                mqttCommand(a);
                break;
            case "sparkplug-node":
                sparkplugNode(a);
                break;
            default:
                status(scenario, "error", "unknown scenario: " + scenario);
                System.exit(3);
        }
        System.exit(0);
    }

    // ---- Paho: the platform broker ------------------------------------------------------------

    /**
     * Connects as an MQTT 3.1.1 client over TLS and publishes one message at the given QoS. For QoS 1
     * and 2 the blocking publish returns only when the broker has acknowledged it, so "published" with
     * qos 1 means the broker sent a PUBACK.
     */
    static void mqttPublish(Map<String, String> a) throws Exception {
        String scenario = "mqtt-publish";
        int qos = Integer.parseInt(a.getOrDefault("qos", "1"));
        MqttClient c = newClient(a);
        c.connect(options(a));
        status(scenario, "connected", "");
        MqttMessage m = new MqttMessage(require(a, "payload").getBytes(StandardCharsets.UTF_8));
        m.setQos(qos);
        m.setRetained(false);
        c.publish(require(a, "topic"), m);
        status(scenario, "published", "qos=" + qos);
        c.disconnect();
        c.close();
    }

    /**
     * Connects, subscribes to the device's command topic at QoS 1 and waits for one command. With
     * --subscribe false it connects and then does NOT subscribe: a device whose command subscription was
     * dropped (negative control N1). With --respond true it answers the command on the device's
     * command-response topic, which is what moves a command to SUCCESSFUL.
     */
    static void mqttCommand(Map<String, String> a) throws Exception {
        String scenario = "mqtt-command";
        boolean subscribe = Boolean.parseBoolean(a.getOrDefault("subscribe", "true"));
        boolean respond = Boolean.parseBoolean(a.getOrDefault("respond", "true"));
        int waitSeconds = Integer.parseInt(a.getOrDefault("wait", "60"));
        String topic = require(a, "topic");
        String respondTopic = a.get("respond-topic");

        MqttClient c = newClient(a);
        CountDownLatch got = new CountDownLatch(1);
        AtomicReference<String> body = new AtomicReference<>("");
        c.setCallback(new MqttCallback() {
            @Override
            public void connectionLost(Throwable cause) {
                status(scenario, "connection-lost", String.valueOf(cause));
            }

            @Override
            public void messageArrived(String t, MqttMessage message) throws Exception {
                String s = new String(message.getPayload(), StandardCharsets.UTF_8);
                body.set(s);
                status(scenario, "received", s);
                if (respond && respondTopic != null) {
                    String cmdToken = jsonField(s, "token");
                    String nonce = jsonField(s, "dispatchNonce");
                    String resp = "{\"commandToken\":\"" + cmdToken + "\",\"dispatchNonce\":\"" + nonce
                            + "\",\"success\":true,\"payload\":\"ok\"}";
                    MqttMessage r = new MqttMessage(resp.getBytes(StandardCharsets.UTF_8));
                    r.setQos(1);
                    c.publish(respondTopic, r);
                    status(scenario, "responded", "");
                }
                got.countDown();
            }

            @Override
            public void deliveryComplete(IMqttDeliveryToken token) {
            }
        });
        c.connect(options(a));
        status(scenario, "connected", "");
        if (subscribe) {
            c.subscribe(topic, 1);
            status(scenario, "subscribed", topic);
        } else {
            // The planted defect: connected, authenticated, and deaf. The line below is the rig's cue
            // that the device is "ready", exactly as "subscribed" is for a healthy one.
            status(scenario, "subscribed", "NOT SUBSCRIBED (planted defect)");
        }
        boolean ok = got.await(waitSeconds, TimeUnit.SECONDS);
        if (!ok) {
            status(scenario, "timeout", "no command within " + waitSeconds + "s");
        }
        // Linger so the response publish has been acknowledged before the connection drops.
        Thread.sleep(500);
        c.disconnect();
        c.close();
        System.exit(ok ? 0 : 4);
    }

    // ---- Tahu: a Sparkplug B edge node --------------------------------------------------------

    /**
     * A Sparkplug B edge node (Eclipse Tahu). It waits for the Host Application's STATE to read ONLINE,
     * publishes an NBIRTH declaring temperature (alias 1) and humidity (alias 2), then NDATA. With
     * --alias-offset N the NDATA addresses alias 1+N instead of 1: a decoder off-by-one (control N2) that
     * a correct decoder attributes to the neighbouring metric.
     */
    static void sparkplugNode(Map<String, String> a) throws Exception {
        String scenario = "sparkplug-node";
        String group = require(a, "group");
        String node = require(a, "node");
        String host = a.getOrDefault("host-id", "conformance-host");
        double temperature = Double.parseDouble(a.getOrDefault("temperature", "21.5"));
        double humidity = Double.parseDouble(a.getOrDefault("humidity", "40.25"));
        long aliasOffset = Long.parseLong(a.getOrDefault("alias-offset", "0"));
        int dataCount = Integer.parseInt(a.getOrDefault("data-count", "3"));
        int waitState = Integer.parseInt(a.getOrDefault("wait-state", "60"));
        String base = "spBv1.0/" + group;
        String nbirth = base + "/NBIRTH/" + node;
        String ndata = base + "/NDATA/" + node;
        String ndeath = base + "/NDEATH/" + node;

        SparkplugBPayloadEncoder enc = new SparkplugBPayloadEncoder();
        long bdSeq = 0;
        MqttClient c = new MqttClient(require(a, "url"), require(a, "client-id"), new MemoryPersistence());
        MqttConnectOptions o = new MqttConnectOptions();
        o.setCleanSession(true);
        o.setAutomaticReconnect(false);
        o.setMqttVersion(MqttConnectOptions.MQTT_VERSION_3_1_1);
        // The will is the node's NDEATH, carrying the bdSeq the NBIRTH declares.
        SparkplugBPayload will = new SparkplugBPayloadBuilder()
                .addMetric(new MetricBuilder("bdSeq", MetricDataType.Int64, bdSeq).createMetric())
                .createPayload();
        o.setWill(ndeath, enc.getBytes(will, false), 0, false);

        CountDownLatch online = new CountDownLatch(1);
        c.setCallback(new MqttCallback() {
            @Override
            public void connectionLost(Throwable cause) {
                status(scenario, "connection-lost", String.valueOf(cause));
            }

            @Override
            public void messageArrived(String t, MqttMessage message) {
                if (t.equals("spBv1.0/STATE/" + host)) {
                    String s = new String(message.getPayload(), StandardCharsets.UTF_8);
                    status(scenario, "state", s);
                    if (s.contains("\"online\":true") || s.contains("\"online\": true") || s.equals("ONLINE")) {
                        online.countDown();
                    }
                } else if (t.contains("/NCMD/")) {
                    status(scenario, "ncmd", t);
                }
            }

            @Override
            public void deliveryComplete(IMqttDeliveryToken token) {
            }
        });
        c.connect(o);
        status(scenario, "connected", "");
        c.subscribe("spBv1.0/STATE/" + host, 1);
        c.subscribe(base + "/NCMD/" + node, 0);
        if (!online.await(waitState, TimeUnit.SECONDS)) {
            status(scenario, "error", "the Host Application's STATE never read ONLINE within " + waitState + "s");
            c.disconnect();
            System.exit(2);
        }

        long seq = 0;
        SparkplugBPayload birth = new SparkplugBPayloadBuilder()
                .setTimestamp(new Date())
                .setSeq(seq++)
                .addMetric(new MetricBuilder("bdSeq", MetricDataType.Int64, bdSeq).createMetric())
                .addMetric(new MetricBuilder("Node Control/Rebirth", MetricDataType.Boolean, false).createMetric())
                .addMetric(new MetricBuilder("temperature", MetricDataType.Double, temperature).alias(1L).createMetric())
                .addMetric(new MetricBuilder("humidity", MetricDataType.Double, humidity).alias(2L).createMetric())
                .createPayload();
        MqttMessage bm = new MqttMessage(enc.getBytes(birth, false));
        bm.setQos(0);
        c.publish(nbirth, bm);
        status(scenario, "born", nbirth);

        // NDATA. Aliases only, no names: the platform has to resolve them against the birth, which is
        // exactly the decoder behaviour under test. A new value on each message, so a single stored
        // reading cannot be mistaken for the birth's own.
        double v = temperature;
        for (int i = 0; i < dataCount; i++) {
            Thread.sleep(700);
            v = temperature + (i + 1);
            SparkplugBPayload data = new SparkplugBPayloadBuilder()
                    .setTimestamp(new Date())
                    .setSeq(seq++ % 256)
                    .addMetric(new MetricBuilder(1L + aliasOffset, MetricDataType.Double, v).createMetric())
                    .createPayload();
            MqttMessage dm = new MqttMessage(enc.getBytes(data, false));
            dm.setQos(0);
            c.publish(ndata, dm);
            status(scenario, "data", "alias=" + (1L + aliasOffset) + " value=" + v);
        }
        Thread.sleep(1000);
        status(scenario, "done", "lastValue=" + v);
        // Hold the session open if asked, so presence stays up for the rig to read; the rig stops the
        // container. Otherwise disconnect cleanly.
        if (Boolean.parseBoolean(a.getOrDefault("hold", "false"))) {
            new CountDownLatch(1).await();
        }
        c.disconnect();
        c.close();
    }

    // ---- shared -------------------------------------------------------------------------------

    static MqttClient newClient(Map<String, String> a) throws Exception {
        return new MqttClient(require(a, "url"), require(a, "client-id"), new MemoryPersistence());
    }

    static MqttConnectOptions options(Map<String, String> a) throws Exception {
        MqttConnectOptions o = new MqttConnectOptions();
        o.setMqttVersion(MqttConnectOptions.MQTT_VERSION_3_1_1);
        o.setCleanSession(true);
        o.setAutomaticReconnect(false);
        o.setConnectionTimeout(20);
        o.setUserName(require(a, "user"));
        o.setPassword(require(a, "password").toCharArray());
        String ca = a.get("ca");
        if (ca != null && !ca.isEmpty()) {
            o.setSocketFactory(trusting(ca));
            // The broker's certificate is issued for the instance's names, not the node address the
            // device reaches it at: the chain is verified, the name is not.
            o.setHttpsHostnameVerificationEnabled(false);
        }
        return o;
    }

    /** A socket factory that trusts exactly the CA in the PEM file, and nothing else. */
    static SSLSocketFactory trusting(String caPath) throws Exception {
        CertificateFactory cf = CertificateFactory.getInstance("X.509");
        KeyStore ks = KeyStore.getInstance(KeyStore.getDefaultType());
        ks.load(null, null);
        try (InputStream in = new FileInputStream(caPath)) {
            int i = 0;
            for (Certificate cert : cf.generateCertificates(in)) {
                ks.setCertificateEntry("ca" + i++, cert);
            }
        }
        TrustManagerFactory tmf = TrustManagerFactory.getInstance(TrustManagerFactory.getDefaultAlgorithm());
        tmf.init(ks);
        SSLContext ctx = SSLContext.getInstance("TLS");
        ctx.init(null, tmf.getTrustManagers(), null);
        return ctx.getSocketFactory();
    }

    /** Pulls a top-level string field out of the small JSON envelope the platform sends. */
    static String jsonField(String json, String name) {
        String key = "\"" + name + "\"";
        int i = json.indexOf(key);
        if (i < 0) {
            return "";
        }
        int colon = json.indexOf(':', i + key.length());
        int q1 = json.indexOf('"', colon + 1);
        int q2 = json.indexOf('"', q1 + 1);
        return (colon < 0 || q1 < 0 || q2 < 0) ? "" : json.substring(q1 + 1, q2);
    }

    static void status(String scenario, String event, String detail) {
        Map<String, String> m = new LinkedHashMap<>();
        m.put("scenario", scenario);
        m.put("event", event);
        if (detail != null && !detail.isEmpty()) {
            m.put("detail", detail);
        }
        System.out.println(SENTINEL + toJson(m));
        System.out.flush();
    }

    static String toJson(Map<String, String> m) {
        StringBuilder b = new StringBuilder("{");
        boolean first = true;
        for (Map.Entry<String, String> e : m.entrySet()) {
            if (!first) {
                b.append(",");
            }
            first = false;
            b.append('"').append(esc(e.getKey())).append("\":\"").append(esc(e.getValue())).append('"');
        }
        return b.append("}").toString();
    }

    static String esc(String s) {
        return s.replace("\\", "\\\\").replace("\"", "\\\"").replace("\n", " ").replace("\r", " ");
    }

    static Map<String, String> parseArgs(String[] args) {
        Map<String, String> m = new HashMap<>();
        for (int i = 0; i + 1 < args.length; i += 2) {
            String k = args[i];
            if (k.startsWith("--")) {
                k = k.substring(2);
            }
            m.put(k, args[i + 1]);
        }
        return m;
    }

    static String require(Map<String, String> m, String k) {
        String v = m.get(k);
        if (v == null) {
            throw new IllegalArgumentException("missing required arg --" + k + " in " + Arrays.toString(m.keySet().toArray()));
        }
        return v;
    }
}
