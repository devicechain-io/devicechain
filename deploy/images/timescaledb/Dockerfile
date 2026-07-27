# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# The TimescaleDB operand image for the `tsdb` store (ADR-020 A2.6, ADR-028).
#
# WHY WE BUILD THIS OURSELVES. Every alternative was measured and every one of
# them ships a database we would not choose:
#
#   - `timescale/timescaledb` and `timescaledb-ha` are architecturally
#     incompatible with CloudNativePG: they carry a Patroni entrypoint and put
#     PGDATA at /home/postgres/pgdata, and CNPG's webhook forbids overriding
#     PGDATA (charts#414, cnpg#5500). The image we run today simply cannot move.
#   - `clevyr/cloudnativepg-timescale` is committed to weekly but its published
#     tags sit at TimescaleDB 2.19.3 / PG 16.9 — a green pipeline shipping old
#     software.
#   - `imusmanmalik/...-timescaledb-postgis` is genuinely maintained, and the A2
#     spike ran a 3-instance cluster on it successfully — but measurement on the
#     running cluster showed TimescaleDB **2.22.0**, BELOW the 2.26.4 failover
#     fix. Research had reported it as tracking 2.28.3; that is not true of the
#     PG 16 tag.
#   - The official CNPG extensions catalog can never serve us: it carries the
#     TimescaleDB *Apache-2* edition, and continuous aggregates are TSL-only.
#     `measurement_rollups` and the whole ADR-026 lifecycle would vanish.
#
# So every community option would have shipped us into a known post-failover
# defect. Owning it is roughly ten lines and makes both pins ours.
#
# LICENSING. TimescaleDB Community Edition is TSL-licensed, not Apache-2.0. We
# already run that build today (`timescale/timescaledb:latest-pg16` IS the
# community edition), so this is not new exposure — but building the image makes
# DeviceChain a *redistributor*, which the TSL permits with notice obligations.
# The licence text is copied into the image below, and NOTICE records the
# obligation. DeviceChain qualifies under the TSL's own "Value Added Product"
# carve-out, whose worked example is an IoT platform. The red line the TSL draws
# is offering raw Timescale to tenants as a database service; DeviceChain does
# not, and must not.

ARG PG_IMAGE=ghcr.io/cloudnative-pg/postgresql:17.10-standard-bookworm
FROM ${PG_IMAGE}

ARG PG_MAJOR=17
ARG TIMESCALEDB_VERSION
ARG TIMESCALEDB_COMPAT_VERSIONS=""

# The base runs as uid 26. apt needs root; the final USER puts it back.
USER root

RUN set -eux; \
    test -n "${TIMESCALEDB_VERSION}" || { echo "TIMESCALEDB_VERSION is required" >&2; exit 1; }; \
    \
    # gnupg is ALREADY in the CNPG base. Record that before installing anything,
    # because `apt-get purge --auto-remove curl gnupg` at the end reads as
    # "remove the tools I installed" and is not: it took gnupg and fifteen of its
    # dependencies out of the base image, turning "the base plus two packages"
    # into "the base MINUS sixteen, plus two". Nothing broke, and nothing would
    # have noticed.
    had_gnupg=no; \
    dpkg-query -W -f='${Status}' gnupg 2>/dev/null | grep -q "ok installed" && had_gnupg=yes; \
    echo "gnupg present in base: ${had_gnupg}"; \
    \
    # The Debian suite is READ FROM THE BASE rather than passed in. Passing it
    # would create a second pin that has to agree with PG_IMAGE, and the failure
    # mode of disagreement is apt quietly resolving a package built for the wrong
    # libc — so the base is asked instead, and cannot be contradicted.
    . /etc/os-release; suite="${VERSION_CODENAME}"; \
    echo "base: ${PRETTY_NAME} (suite=${suite})"; \
    \
    apt-get update; \
    apt-get install -y --no-install-recommends ca-certificates curl gnupg; \
    curl -fsSL https://packagecloud.io/timescale/timescaledb/gpgkey \
      | gpg --dearmor -o /usr/share/keyrings/timescaledb.gpg; \
    echo "deb [signed-by=/usr/share/keyrings/timescaledb.gpg] https://packagecloud.io/timescale/timescaledb/debian/ ${suite} main" \
      > /etc/apt/sources.list.d/timescaledb.list; \
    apt-get update; \
    \
    # 🔴 THE LOADER IS PINNED, and this is the least obvious line in the file.
    #
    # `timescaledb.so` is a stub. The real code lives in versioned libraries
    # (`timescaledb-<v>.so`), and the stub loads whichever one matches the
    # database's catalog version. The stub — and, decisively, the .control file
    # that declares `default_version` — is owned by the *loader* package, not by
    # the versioned ones (verified with `dpkg -S`).
    #
    # The versioned packages depend on the loader only as `>= <version>`, so an
    # unpinned install takes the NEWEST loader in the repository. That is fine
    # today and breaks silently on the day Timescale publishes the next release:
    # the rebuilt image would declare `default_version = 2.29.0` while carrying
    # only 2.28.3's library, so `CREATE EXTENSION timescaledb` looks for a
    # `$libdir/timescaledb-2.29.0` that is not there. Nothing in this file would
    # have changed. Pinning the loader is what makes the image reproducible.
    # Word splitting on the version list is intended; pathname expansion is not.
    set -f; \
    versions="${TIMESCALEDB_VERSION} ${TIMESCALEDB_COMPAT_VERSIONS}"; \
    resolve() { \
      apt-cache madison "$1" | awk -v v="$2~" '$3 ~ "^"v {print $3; exit}'; \
    }; \
    loader_pkg="timescaledb-2-loader-postgresql-${PG_MAJOR}"; \
    loader_ver="$(resolve "${loader_pkg}" "${TIMESCALEDB_VERSION}")"; \
    test -n "${loader_ver}" || { echo "no ${loader_pkg} for ${TIMESCALEDB_VERSION} on ${suite}" >&2; exit 1; }; \
    install_list="${loader_pkg}=${loader_ver}"; \
    for v in ${versions}; do \
      pkg="timescaledb-2-${v}-postgresql-${PG_MAJOR}"; \
      ver="$(resolve "${pkg}" "${v}")"; \
      test -n "${ver}" || { echo "no ${pkg} for ${v} on ${suite}" >&2; exit 1; }; \
      install_list="${install_list} ${pkg}=${ver}"; \
    done; \
    echo "installing: ${install_list}"; \
    apt-get install -y --no-install-recommends ${install_list}; \
    set +f; \
    \
    # Ship the licence text we are obliged to pass downstream.
    #
    # 🔴 The Debian package's `copyright` file is NOT the licence. It is a DEP-5
    # stub whose `Files: *` stanza says Apache-2.0 and whose `tsl/*` stanza gives
    # a URL to the Timescale License rather than its text. Shipping only that
    # while NOTICE claims "the licence text is included in the image" would make
    # the notice false. Fetch the real agreement, at the tag matching the version
    # actually installed, and keep the Debian copyright alongside it because it
    # is the per-file license map.
    install -d /usr/share/licenses/timescaledb; \
    cp "/usr/share/doc/timescaledb-2-${TIMESCALEDB_VERSION}-postgresql-${PG_MAJOR}/copyright" \
       /usr/share/licenses/timescaledb/copyright; \
    curl -fsSL -o /usr/share/licenses/timescaledb/LICENSE-TIMESCALE \
      "https://raw.githubusercontent.com/timescale/timescaledb/${TIMESCALEDB_VERSION}/tsl/LICENSE-TIMESCALE"; \
    \
    # Remove the apt source and its key. Left in place they are a loaded gun:
    # any later `apt-get install` or `apt-get upgrade` in a derived image, a
    # debug session or a scanner's remediation step would pull a NEWER
    # TimescaleDB, defeating the pin this whole design rests on.
    rm -f /etc/apt/sources.list.d/timescaledb.list /usr/share/keyrings/timescaledb.gpg; \
    \
    # Purge only what this build added — see the had_gnupg note above.
    apt-get purge -y --auto-remove curl; \
    if [ "${had_gnupg}" = "no" ]; then apt-get purge -y --auto-remove gnupg; fi; \
    rm -rf /var/lib/apt/lists/*

# The build asserts its own output. A missing library is not detectable by
# looking at the Dockerfile — it is a fact about the filesystem — and the way it
# surfaces otherwise is a replica that will not start, hours later, mid-rollout.
RUN set -eux; \
    set -f; \
    libdir="$(pg_config --pkglibdir)"; \
    for v in ${TIMESCALEDB_VERSION} ${TIMESCALEDB_COMPAT_VERSIONS}; do \
      test -f "${libdir}/timescaledb-${v}.so" \
        || { echo "MISSING ${libdir}/timescaledb-${v}.so" >&2; exit 1; }; \
      # The TSL library is the one that carries continuous aggregates,
      # compression and the job scheduler. An Apache-edition build would leave
      # this absent and pass every other check in this file.
      test -f "${libdir}/timescaledb-tsl-${v}.so" \
        || { echo "MISSING TSL library ${libdir}/timescaledb-tsl-${v}.so — Apache-edition build?" >&2; exit 1; }; \
      echo "ok: timescaledb-${v}.so + timescaledb-tsl-${v}.so"; \
    done; \
    test -f "${libdir}/timescaledb.so" || { echo "MISSING stub loader" >&2; exit 1; }; \
    control="$(pg_config --sharedir)/extension/timescaledb.control"; \
    got="$(awk -F\' '/^default_version/ {print $2}' "${control}")"; \
    test "${got}" = "${TIMESCALEDB_VERSION}" \
      || { echo "default_version is ${got}, expected ${TIMESCALEDB_VERSION} — the loader pin did not hold" >&2; exit 1; }; \
    echo "ok: default_version = ${got}"; \
    \
    # The licence file NOTICE points at. Asserted because the obligation is
    # legal rather than functional: nothing would ever fail if it were missing,
    # which is exactly why it needs a check rather than a copy step.
    lic=/usr/share/licenses/timescaledb/LICENSE-TIMESCALE; \
    test -s "${lic}" || { echo "MISSING ${lic} — NOTICE claims the image carries it" >&2; exit 1; }; \
    grep -qi "TIMESCALE LICENSE" "${lic}" \
      || { echo "${lic} does not look like the Timescale License" >&2; exit 1; }; \
    echo "ok: TSL text present ($(wc -c < "${lic}") bytes)"; \
    \
    # The pin is only a pin while the repository that could override it is gone.
    for leftover in /etc/apt/sources.list.d/timescaledb.list /usr/share/keyrings/timescaledb.gpg; do \
      test ! -e "${leftover}" \
        || { echo "LEFTOVER ${leftover} — a later apt-get could upgrade TimescaleDB past the pin" >&2; exit 1; }; \
    done; \
    echo "ok: packagecloud apt source and keyring removed"

# Record the pins so a running container can be asked what it is, without
# needing the build context. `dr-rig.sh` and the A2.4 oracles read these.
LABEL io.devicechain.timescaledb.version="${TIMESCALEDB_VERSION}"
LABEL io.devicechain.timescaledb.compat_versions="${TIMESCALEDB_COMPAT_VERSIONS}"
LABEL io.devicechain.postgresql.major="${PG_MAJOR}"
LABEL org.opencontainers.image.source="https://github.com/devicechain-io/devicechain"
LABEL org.opencontainers.image.description="PostgreSQL ${PG_MAJOR} + TimescaleDB ${TIMESCALEDB_VERSION} operand image for CloudNativePG (ADR-020 A2.6). Contains TimescaleDB Community Edition, which is TSL-licensed, not Apache-2.0."

USER 26
