-- Copyright The DeviceChain Authors
-- SPDX-License-Identifier: Apache-2.0
--
-- Functional gate for the operand image (ADR-020 A2.6).
--
-- Every statement here asserts. Nothing merely prints: a smoke test whose output
-- a human is supposed to read is a smoke test nobody reads, and this one runs in
-- CI where there is no human. Run with `-v ON_ERROR_STOP=1`, which turns the
-- RAISE EXCEPTIONs below into a nonzero psql exit.
--
-- What this is really guarding: an image that installs cleanly, starts, and is
-- missing exactly the features DeviceChain depends on. That is not hypothetical
-- — the official CNPG extensions catalog ships the TimescaleDB *Apache-2*
-- edition, on which continuous aggregates, compression and the job scheduler do
-- not exist at all. `CREATE EXTENSION` succeeds there. `measurement_rollups`
-- (ADR-026) does not.
--
-- Expects: -v want_pg_major=17 -v want_ts_version=2.28.3

\set ON_ERROR_STOP on

-- psql does NOT substitute :'vars' inside dollar-quoted bodies — the lexer treats
-- $$...$$ as an opaque literal, so `:'want_pg_major'` there is a syntax error
-- rather than a value. Park the expectations in session GUCs out here, where
-- substitution does happen, and read them back with current_setting() inside the
-- blocks. (Worth knowing: without ON_ERROR_STOP, psql exits 0 even after a
-- syntax error, so getting this wrong yields a green, entirely vacuous run.)
SELECT set_config('dc.want_pg_major',   :'want_pg_major',   false);
SELECT set_config('dc.want_ts_version', :'want_ts_version', false);

-- 1. The server is the PostgreSQL major we pinned. -----------------------------
DO $$
DECLARE got int; want int := current_setting('dc.want_pg_major')::int;
BEGIN
  SELECT current_setting('server_version_num')::int / 10000 INTO got;
  IF got <> want THEN
    RAISE EXCEPTION 'PostgreSQL major is %, expected %', got, want;
  END IF;
END $$;

-- 2. TimescaleDB is loadable, and is the version we pinned. --------------------
-- CREATE EXTENSION is the moment the stub loader resolves `timescaledb-<v>.so`,
-- so this is also where a missing versioned library surfaces.
CREATE EXTENSION IF NOT EXISTS timescaledb;

DO $$
DECLARE got text; want text := current_setting('dc.want_ts_version');
BEGIN
  SELECT extversion INTO got FROM pg_extension WHERE extname = 'timescaledb';
  IF got IS DISTINCT FROM want THEN
    RAISE EXCEPTION 'TimescaleDB extension is %, expected %', COALESCE(got, '<absent>'), want;
  END IF;
END $$;

-- 3. The version is at or above the post-failover fix. -------------------------
-- timescale#9360: a background job that is MID-RUN when its node fails comes
-- back with next_start at the -infinity sentinel and never runs again. The
-- database reports healthy while aggregation, retention and compression have
-- silently stopped. Fixed in 2.26.4. This assertion is the reason the image
-- exists at all -- every community operand image we measured was below it.
DO $$
DECLARE
  got text;
  got_parts int[];
  min_parts int[] := ARRAY[2, 26, 4];
BEGIN
  SELECT extversion INTO got FROM pg_extension WHERE extname = 'timescaledb';
  SELECT array_agg(p::int ORDER BY ord)
    INTO got_parts
    FROM unnest(string_to_array(split_part(got, '-', 1), '.')) WITH ORDINALITY AS t(p, ord);

  IF got_parts[1:3] < min_parts THEN
    RAISE EXCEPTION
      'TimescaleDB % is below 2.26.4, which carries the fix for the -infinity next_start defect (timescale#9360)', got;
  END IF;
END $$;

-- 4. Hypertables. --------------------------------------------------------------
CREATE TABLE smoke_measurement (
  occurred_time timestamptz      NOT NULL,
  device_token  text             NOT NULL,
  value         double precision NOT NULL
);
SELECT create_hypertable('smoke_measurement', 'occurred_time');

INSERT INTO smoke_measurement
SELECT now() - (i || ' minutes')::interval, 'device-' || (i % 4), i
FROM generate_series(1, 2000) AS i;

DO $$
DECLARE chunks int;
BEGIN
  SELECT count(*) INTO chunks
    FROM timescaledb_information.chunks
   WHERE hypertable_name = 'smoke_measurement';
  IF chunks < 1 THEN
    RAISE EXCEPTION 'hypertable produced % chunks, expected at least 1', chunks;
  END IF;
END $$;

-- 5. THE TSL CANARY: a continuous aggregate. -----------------------------------
-- This is `measurement_rollups` (ADR-026) in miniature. On an Apache-edition
-- build the syntax below does not exist, so this single statement is what stands
-- between us and shipping a database that silently cannot do the thing the
-- data-lifecycle design is built on.
CREATE MATERIALIZED VIEW smoke_rollup
WITH (timescaledb.continuous) AS
  SELECT time_bucket('1 hour', occurred_time) AS bucket,
         device_token,
         avg(value) AS avg_value,
         count(*)   AS sample_count
    FROM smoke_measurement
   GROUP BY bucket, device_token
  WITH NO DATA;

CALL refresh_continuous_aggregate('smoke_rollup', NULL, NULL);

DO $$
DECLARE rollup_rows int; total bigint;
BEGIN
  SELECT count(*) INTO rollup_rows FROM smoke_rollup;
  IF rollup_rows < 1 THEN
    RAISE EXCEPTION 'continuous aggregate refreshed to % rows, expected at least 1', rollup_rows;
  END IF;

  -- Positive control on the aggregate's CONTENT, not just its row count: an
  -- aggregate that materialises rows but drops samples would satisfy the check
  -- above. Every one of the 2000 inserted rows must be accounted for.
  SELECT sum(sample_count) INTO total FROM smoke_rollup;
  IF total <> 2000 THEN
    RAISE EXCEPTION 'continuous aggregate covers % samples, expected 2000', total;
  END IF;
END $$;

-- 6. The other TSL features ADR-026 depends on. --------------------------------
SELECT add_retention_policy('smoke_measurement', INTERVAL '30 days');
ALTER TABLE smoke_measurement
  SET (timescaledb.compress, timescaledb.compress_segmentby = 'device_token');
SELECT add_compression_policy('smoke_measurement', INTERVAL '7 days');
SELECT add_continuous_aggregate_policy('smoke_rollup',
         start_offset => INTERVAL '1 day',
         end_offset   => INTERVAL '1 hour',
         schedule_interval => INTERVAL '1 hour');

DO $$
DECLARE
  retention int; compression int; refresh int;
BEGIN
  SELECT count(*) INTO retention   FROM timescaledb_information.jobs WHERE proc_name = 'policy_retention';
  SELECT count(*) INTO compression FROM timescaledb_information.jobs WHERE proc_name = 'policy_compression';
  SELECT count(*) INTO refresh     FROM timescaledb_information.jobs WHERE proc_name = 'policy_refresh_continuous_aggregate';
  IF retention < 1 OR compression < 1 OR refresh < 1 THEN
    RAISE EXCEPTION 'job scheduler incomplete: retention=% compression=% cagg_refresh=%',
      retention, compression, refresh;
  END IF;
END $$;

-- NOTE for A2.4, deliberately NOT asserted here: a freshly created job reports
-- `next_start IS NULL` until the scheduler computes it. That is "not yet
-- computed", not "stuck", and an oracle that treats the two alike raises a false
-- alarm during exactly the window it was built for -- which is how an oracle
-- gets switched off. Only `-infinity`, or a NULL that persists past a settle
-- period, is a verdict.

-- 7. Extension inheritance through template1. ----------------------------------
-- DeviceChain services self-provision their own databases at startup (decision
-- D2), so they land on a database created AFTER the cluster was bootstrapped.
-- The upstream `timescale/timescaledb` image puts the extension in template1 and
-- DeviceChain silently depends on that inheritance.
--
-- ⚠️ Read this for what it is. `standalone.sh` has ALREADY put timescaledb into
-- template1 before this file runs, so what follows demonstrates that PostgreSQL
-- template inheritance carries the extension on THIS image — it does not, and
-- cannot, verify that the A2.4 Cluster spec sets `postInitTemplateSQL`. That is
-- a property of a manifest, and it needs its own check in A2.4. The value here
-- is narrower than it looks: it catches an image where the extension cannot be
-- installed into template1 at all.
CREATE EXTENSION IF NOT EXISTS timescaledb; -- no-op here; the real work is below
\c template1
CREATE EXTENSION IF NOT EXISTS timescaledb;
\c postgres
CREATE DATABASE smoke_inherited;
\c smoke_inherited

DO $$
DECLARE got text;
BEGIN
  SELECT extversion INTO got FROM pg_extension WHERE extname = 'timescaledb';
  IF got IS NULL THEN
    RAISE EXCEPTION
      'a database created after template1 carried timescaledb did NOT inherit it — services that self-provision would come up without hypertable support';
  END IF;
END $$;

-- A hypertable in the inherited database, because "the extension is listed" and
-- "the extension works" are different claims.
CREATE TABLE inherited_check (t timestamptz NOT NULL, v int);
SELECT create_hypertable('inherited_check', 't');

\echo 'operand image smoke: PASS'
