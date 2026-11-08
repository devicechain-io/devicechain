# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# The Paho and Tahu clients of the transport conformance rig (hack/conformance-rig.sh). Built from
# source in a container, so the rig needs no JDK on the host. Same bases, pinned by digest, as the
# Leshan interop client (backend/services/lwm2m-ingest/interop/leshan-client/Dockerfile): this image
# ships nowhere, so a base frozen at a known digest is the point. Java 17 here, not 11, because Tahu's
# current releases are 17 bytecode. Bump the digests by hand.
FROM maven:3.9-eclipse-temurin-17@sha256:1a352420f7aba21f5ad08df31bab55f74c013fb491f1ae8ab1dd7ff9ed698584 AS build
WORKDIR /build
COPY pom.xml .
COPY src ./src
RUN mvn -q -B -DskipTests package

FROM eclipse-temurin:17-jre@sha256:2a996a534890f1bee252fab23a0c01e773e9e1dc0e4700e8f0f6a2697c6cbf76
RUN useradd --uid 10001 --create-home conformance
USER 10001
COPY --from=build /build/target/conformance-client.jar /app/conformance-client.jar
ENTRYPOINT ["java", "-jar", "/app/conformance-client.jar"]
