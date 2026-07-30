# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Container image for the Eclipse Leshan interop client, used by the LwM2M full-stack
# interop test (Shape B): it runs IN-cluster as a Job/Pod and drives a real DTLS-PSK
# LwM2M client against a DEPLOYED lwm2m-ingest over the ClusterIP UDP Service — the one
# path no gate exercises (config.Bindings/ResolveCredentials/the Helm UDP Service/main.go
# composition/the lease). The same jar backs the in-process slice-1 interop test.
#
# Leshan 2.0.0-M15 is Java-8 bytecode, so Temurin 11 builds and runs it. Multi-stage so
# the image is reproducible from source with no host toolchain (what CI needs).
FROM maven:3.8-eclipse-temurin-11 AS build
WORKDIR /build
COPY pom.xml .
COPY src ./src
RUN mvn -q -B -DskipTests package

FROM eclipse-temurin:11-jre
# Run unprivileged; the client needs no filesystem or root.
RUN useradd --uid 10001 --create-home leshan
USER 10001
COPY --from=build /build/target/leshan-interop-client.jar /app/leshan-interop-client.jar
# Args (--server host:port --identity <id> --psk-b64 <b64> --scenario <name>) come from
# the Pod/Job spec. The client exits non-zero if it cannot register within its window,
# so a Pod restartPolicy: OnFailure retries until the single-replica server is leader +
# serving (its /readyz opens BEFORE it binds the UDP socket, so "pod Ready" is not
# "serving" — retrying the real handshake is the only correct readiness gate).
ENTRYPOINT ["java", "-jar", "/app/leshan-interop-client.jar"]
