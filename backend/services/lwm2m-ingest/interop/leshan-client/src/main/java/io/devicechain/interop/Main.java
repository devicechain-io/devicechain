// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package io.devicechain.interop;

import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.Base64;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;

import org.eclipse.californium.core.config.CoapConfig;
import org.eclipse.californium.elements.config.Configuration;
import org.eclipse.californium.scandium.config.DtlsConfig;
import org.eclipse.californium.scandium.dtls.cipher.CipherSuite;
import org.eclipse.leshan.client.LeshanClient;
import org.eclipse.leshan.client.LeshanClientBuilder;
import org.eclipse.leshan.client.californium.endpoint.CaliforniumClientEndpointsProvider;
import org.eclipse.leshan.client.californium.endpoint.coaps.CoapsClientProtocolProvider;
import org.eclipse.leshan.client.object.Security;
import org.eclipse.leshan.client.object.Server;
import org.eclipse.leshan.client.observer.LwM2mClientObserverAdapter;
import org.eclipse.leshan.client.resource.BaseInstanceEnabler;
import org.eclipse.leshan.client.resource.LwM2mObjectEnabler;
import org.eclipse.leshan.client.resource.ObjectsInitializer;
import org.eclipse.leshan.client.servers.LwM2mServer;
import org.eclipse.leshan.core.LwM2mId;
import org.eclipse.leshan.core.ResponseCode;
import org.eclipse.leshan.core.model.LwM2mModel;
import org.eclipse.leshan.core.model.ObjectLoader;
import org.eclipse.leshan.core.model.ObjectModel;
import org.eclipse.leshan.core.model.ResourceModel;
import org.eclipse.leshan.core.model.StaticModel;
import org.eclipse.leshan.core.node.LwM2mResource;
import org.eclipse.leshan.core.node.codec.DefaultLwM2mEncoder;
import org.eclipse.leshan.core.node.codec.LwM2mValueConverter;
import org.eclipse.leshan.core.node.codec.NodeEncoder;
import org.eclipse.leshan.core.node.codec.PathEncoder;
import org.eclipse.leshan.core.request.ContentFormat;
import org.eclipse.leshan.core.request.argument.Arguments;
import org.eclipse.leshan.core.response.ExecuteResponse;
import org.eclipse.leshan.core.response.WriteResponse;
import org.eclipse.leshan.core.request.DeregisterRequest;
import org.eclipse.leshan.core.request.RegisterRequest;
import org.eclipse.leshan.core.request.UpdateRequest;
import org.eclipse.leshan.core.response.ReadResponse;

/**
 * The Eclipse Leshan interop client (Leshan 2.x). It registers against DeviceChain's lwm2m-ingest
 * over DTLS-PSK (cipher TLS_PSK_WITH_AES_128_CCM_8, no CID), as LwM2M 1.1 so it answers SenML-JSON
 * observes, and drives one scenario, emitting sentinel-prefixed JSON status lines on STDOUT (all
 * logging goes to stderr). The Go harness reads the truth from the server side; these lines are only
 * diagnostics.
 */
public final class Main {

    static final String SENTINEL = "DCINTEROP ";
    static final int SHORT_SERVER_ID = 123;
    static final long CLIENT_LIFETIME = 300; // long: the server clamps expiry; the client won't auto-Update in our windows
    static final int TEMP_OBJECT_ID = 3303;  // IPSO Temperature
    static final int BIG_OBJECT_ID = 3441;    // top of the IPSO allowlist range (3200..3441)
    static final int BASE_RES = 5700;         // IPSO "Sensor Value"; Block2 object uses BASE_RES..BASE_RES+count-1

    public static void main(String[] args) throws Exception {
        Map<String, String> a = parseArgs(args);
        String server = require(a, "server");        // host:port
        String identity = require(a, "identity");     // the DTLS-PSK identity (tenancy comes from THIS)
        // The registration endpoint name. It defaults to the PSK identity, but a scenario can set it to
        // a DIFFERENT (even another tenant's) value to prove D1: tenancy is bound to the authenticated
        // PSK identity, never to the `ep` the device asserts in the registration.
        String endpoint = a.getOrDefault("endpoint", identity);
        byte[] psk = Base64.getDecoder().decode(require(a, "psk-b64"));
        String scenario = require(a, "scenario");
        double value = Double.parseDouble(a.getOrDefault("value", "21.5"));
        int blockCount = Integer.parseInt(a.getOrDefault("count", "100"));

        String serverUri = "coaps://" + server;

        // loadDefault() ships only the base LwM2M objects (0..7), not IPSO sensors, so define the two
        // objects this client hosts ourselves — self-contained, no dependency on bundled DDF resources.
        List<ObjectModel> models = new ArrayList<>(ObjectLoader.loadDefault());
        models.removeIf(m -> m.id == TEMP_OBJECT_ID || m.id == BIG_OBJECT_ID);
        models.add(temperatureModel());
        models.add(bigSensorModel(blockCount));
        LwM2mModel model = new StaticModel(models);

        ObjectsInitializer init = new ObjectsInitializer(model);
        init.setInstancesForObject(LwM2mId.SECURITY,
                Security.psk(serverUri, SHORT_SERVER_ID, identity.getBytes(StandardCharsets.UTF_8), psk));
        init.setInstancesForObject(LwM2mId.SERVER, new Server(SHORT_SERVER_ID, CLIENT_LIFETIME));
        init.setInstancesForObject(LwM2mId.DEVICE, new SimpleDevice());

        // create() ONLY the objects this client hosts — createAll() would try to instantiate every
        // base object in the model (Access Control, Firmware, ...) and fail with no factory for them.
        // Keep concrete references so the observe scenarios can fire real value-change notifications.
        TemperatureSensor temp = new TemperatureSensor(value);
        BigSensor big = new BigSensor(blockCount);
        List<LwM2mObjectEnabler> enablers;
        if (scenario.equals("observe-block2")) {
            init.setInstancesForObject(BIG_OBJECT_ID, big);
            enablers = init.create(LwM2mId.SECURITY, LwM2mId.SERVER, LwM2mId.DEVICE, BIG_OBJECT_ID);
        } else {
            init.setInstancesForObject(TEMP_OBJECT_ID, temp);
            enablers = init.create(LwM2mId.SECURITY, LwM2mId.SERVER, LwM2mId.DEVICE, TEMP_OBJECT_ID);
        }

        // DTLS/CoAPS endpoint (Californium 3.x): offer ONLY the server's hard-pinned suite; leave CID
        // off (slice 1). The PSK store is wired by Leshan from the Security object above. Force a small
        // message/block size so the Block2 scenario ALWAYS fragments regardless of upstream defaults —
        // the ~2KB SenML pack becomes ≥8 blocks, so the test proves reassembly, not a lucky one-datagram.
        CaliforniumClientEndpointsProvider.Builder epBuilder =
                new CaliforniumClientEndpointsProvider.Builder(new CoapsClientProtocolProvider());
        Configuration config = epBuilder.createDefaultConfiguration();
        config.set(DtlsConfig.DTLS_CIPHER_SUITES, Arrays.asList(CipherSuite.TLS_PSK_WITH_AES_128_CCM_8));
        config.set(CoapConfig.MAX_MESSAGE_SIZE, 256);
        config.set(CoapConfig.PREFERRED_BLOCK_SIZE, 256);
        epBuilder.setConfiguration(config);
        CaliforniumClientEndpointsProvider endpoints = epBuilder.build();

        LeshanClientBuilder builder = new LeshanClientBuilder(endpoint);
        builder.setObjects(enablers);
        builder.setEndpointsProviders(endpoints);
        if (scenario.equals("tlv-only")) {
            // A LwM2M 1.0-era client: it can encode TLV, text and opaque, and NOT SenML. Asked to Observe
            // in SenML-JSON it has no encoder for the answer, so it refuses (4.06) and sends no telemetry.
            // That is correct client behaviour; the platform has to cope with it, and the matrix says what
            // it does (registers, drives presence, accepts commands, reports no telemetry).
            Map<ContentFormat, NodeEncoder> nodeEncoders = new HashMap<>(DefaultLwM2mEncoder.getDefaultNodeEncoders(true));
            nodeEncoders.keySet().removeIf(f -> !(f.equals(ContentFormat.TLV) || f.equals(ContentFormat.TEXT)
                    || f.equals(ContentFormat.OPAQUE)));
            Map<ContentFormat, PathEncoder> pathEncoders = new HashMap<>(DefaultLwM2mEncoder.getDefaultPathEncoder());
            builder.setEncoder(new DefaultLwM2mEncoder(nodeEncoders, pathEncoders, new DefaultConverter().converter()));
        }
        LeshanClient client = builder.build();

        CountDownLatch registered = new CountDownLatch(1);
        client.addObserver(new LwM2mClientObserverAdapter() {
            @Override
            public void onRegistrationSuccess(LwM2mServer server, RegisterRequest request, String registrationID) {
                status(scenario, "registered", registrationID, "");
                registered.countDown();
            }

            @Override
            public void onRegistrationFailure(LwM2mServer server, RegisterRequest request,
                    ResponseCode responseCode, String errorMessage, Exception cause) {
                status(scenario, "error", "", "registration failed: " + responseCode + " " + errorMessage);
            }

            @Override
            public void onUpdateSuccess(LwM2mServer server, UpdateRequest request) {
                status(scenario, "updated", "", "");
            }

            @Override
            public void onDeregistrationSuccess(LwM2mServer server, DeregisterRequest request) {
                status(scenario, "deregistered", "", "");
            }
        });

        client.start();
        if (!registered.await(30, TimeUnit.SECONDS)) {
            status(scenario, "error", "", "did not register within 30s");
            client.destroy(false);
            System.exit(2);
        }

        switch (scenario) {
            case "lifecycle":
                Thread.sleep(500);
                client.triggerRegistrationUpdate(); // update-keeps-session
                Thread.sleep(1000);
                client.destroy(true); // deregister → DISCONNECTED
                status(scenario, "done", "", "");
                break;

            case "observe":
                // The initial Observe response carries the starting value (21.5). Now drive REAL
                // async notifications by changing the value — so the steady-state Notify path (not just
                // the initial read) is decoded, and a broken/one-shot observation cannot pass.
                for (int i = 1; i <= 3; i++) {
                    Thread.sleep(400);
                    temp.set(value + i); // 22.5, 23.5, 24.5 — each fires a Notify
                }
                holdUntilKilled();
                break;

            case "observe-block2":
                // The initial Observe response of /3441/0 is one big SenML pack (Block2). Then fire a
                // change so a blockwise NOTIFICATION is reassembled too, not just the initial response.
                Thread.sleep(400);
                big.fireResourceChange(BASE_RES);
                holdUntilKilled();
                break;

            case "tlv-only":
                // Registered as a client that cannot produce SenML. The platform's Observe is refused; the
                // harness asserts no telemetry is stored and that presence still reads CONNECTED. The
                // value keeps changing, so a client that COULD notify would have something to say.
                for (int i = 1; ; i++) {
                    Thread.sleep(3000);
                    temp.set(value + 100 + i);
                }

            case "observe-hold":
                // The conformance rig's client (hack/conformance-rig.sh). Like "observe", but it keeps
                // changing the value for as long as it lives, so a notification that follows the
                // platform's Observe is certain to exist however slowly the platform sets it up, and it
                // answers the platform's own Read, Write and Execute on the Device object. The rig judges
                // by what the platform stores and records, never by what this client prints.
                for (int i = 1; ; i++) {
                    Thread.sleep(3000);
                    temp.set(value + 100 + i);
                }

            case "register-hold":
                // Register and hold with NO Update and NO Deregister; the harness kills us and asserts
                // the server marks the device offline on lifetime expiry.
                holdUntilKilled();
                break;

            default:
                status(scenario, "error", "", "unknown scenario: " + scenario);
                client.destroy(false);
                System.exit(3);
        }
        System.exit(0);
    }

    /** Exposes the default encoder's value converter, which is protected, to build a narrower encoder. */
    static final class DefaultConverter extends DefaultLwM2mEncoder {
        LwM2mValueConverter converter() {
            return converter;
        }
    }

    static void holdUntilKilled() throws InterruptedException {
        new CountDownLatch(1).await(); // blocks until the JVM is killed by the harness
    }

    // temperatureModel is IPSO Temperature (3303) reduced to its single numeric Sensor Value (5700).
    static ObjectModel temperatureModel() {
        ResourceModel r = new ResourceModel(BASE_RES, "Sensor Value", ResourceModel.Operations.R,
                false, false, ResourceModel.Type.FLOAT, null, null, null);
        return new ObjectModel(TEMP_OBJECT_ID, "Temperature", null, ObjectModel.DEFAULT_VERSION, false, false,
                Arrays.asList(r));
    }

    // bigSensorModel is a custom object with `count` single-instance numeric resources, so reading the
    // whole instance yields a SenML pack large enough to force CoAP Block2 fragmentation.
    static ObjectModel bigSensorModel(int count) {
        List<ResourceModel> rs = new ArrayList<>();
        for (int i = 0; i < count; i++) {
            rs.add(new ResourceModel(BASE_RES + i, "v" + i, ResourceModel.Operations.R,
                    false, false, ResourceModel.Type.FLOAT, null, null, null));
        }
        return new ObjectModel(BIG_OBJECT_ID, "BigSensor", null, ObjectModel.DEFAULT_VERSION, false, false, rs);
    }

    static void status(String scenario, String event, String regId, String detail) {
        Map<String, String> m = new LinkedHashMap<>();
        m.put("scenario", scenario);
        m.put("event", event);
        if (regId != null && !regId.isEmpty()) {
            m.put("regId", regId);
        }
        if (detail != null && !detail.isEmpty()) {
            m.put("detail", detail);
        }
        System.out.println(SENTINEL + toJson(m));
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
        return s.replace("\\", "\\\\").replace("\"", "\\\"");
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
            throw new IllegalArgumentException("missing required arg --" + k);
        }
        return v;
    }

    // --- object enablers ---------------------------------------------------

    // NOTE: these enablers are public with a public no-arg constructor because Leshan's
    // ObjectsInitializer builds a factory from the class (for hypothetical server-initiated Creates),
    // and the factory lookup requires a public default constructor even though the stateful instance
    // we register is the one actually used for reads.

    /** A minimal LwM2M Device (object 3) so registration has a well-formed object list. */
    public static final class SimpleDevice extends BaseInstanceEnabler {
        // Time Zone (/3/0/15) is the Device object's read-write resource, so a platform Write has
        // something real to change and a Read afterwards something real to prove it changed.
        private volatile String timezone = "Etc/UTC";

        public SimpleDevice() {
        }

        // Reboot (/3/0/4) is the one Execute the Device object offers that is safe to accept: this client
        // answers 2.04 and does nothing, so the platform has a real device response to settle a command on.
        @Override
        public ExecuteResponse execute(LwM2mServer server, int resourceId, Arguments arguments) {
            if (resourceId == 4) {
                status("observe-hold", "executed", "", "/3/0/4");
                return ExecuteResponse.success();
            }
            return super.execute(server, resourceId, arguments);
        }

        @Override
        public WriteResponse write(LwM2mServer server, boolean replace, int resourceId, LwM2mResource value) {
            if (resourceId == 15) {
                timezone = String.valueOf(value.getValue());
                status("observe-hold", "written", "", "/3/0/15=" + timezone);
                fireResourceChange(15);
                return WriteResponse.success();
            }
            return super.write(server, replace, resourceId, value);
        }

        @Override
        public ReadResponse read(LwM2mServer server, int resourceId) {
            switch (resourceId) {
                case 0:
                    return ReadResponse.success(0, "DeviceChain");
                case 1:
                    return ReadResponse.success(1, "InteropClient");
                case 2:
                    return ReadResponse.success(2, "leshan-interop");
                case 15:
                    return ReadResponse.success(15, timezone);
                case 16:
                    return ReadResponse.success(16, "U");
                default:
                    return ReadResponse.notFound();
            }
        }
    }

    /** IPSO Temperature (3303) with a single mutable numeric Sensor Value (5700). */
    public static final class TemperatureSensor extends BaseInstanceEnabler {
        private volatile double value;

        public TemperatureSensor() {
            this(0.0);
        }

        public TemperatureSensor(double value) {
            this.value = value;
        }

        /** Set the value and notify observers — the steady-state Notify path. */
        public void set(double v) {
            this.value = v;
            fireResourceChange(BASE_RES);
        }

        @Override
        public ReadResponse read(LwM2mServer server, int resourceId) {
            if (resourceId == BASE_RES) {
                return ReadResponse.success(BASE_RES, value);
            }
            return ReadResponse.notFound();
        }
    }

    /** A custom object (3441) with `count` single numeric resources (BASE_RES..BASE_RES+count-1). */
    public static final class BigSensor extends BaseInstanceEnabler {
        private final int count;

        public BigSensor() {
            this(0);
        }

        public BigSensor(int count) {
            this.count = count;
        }

        @Override
        public ReadResponse read(LwM2mServer server, int resourceId) {
            if (resourceId >= BASE_RES && resourceId < BASE_RES + count) {
                return ReadResponse.success(resourceId, (double) (resourceId - BASE_RES));
            }
            return ReadResponse.notFound();
        }
    }

    private Main() {
    }
}
