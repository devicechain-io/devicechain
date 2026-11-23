# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# State lives in the fleet manager's Postgres, one workspace per cluster in one schema per provider.
# The schema, tables and indexes are PRE-CREATED at onboarding and owned by the provider's role, so a
# per-run login role that inherits it owns nothing; all three creation steps are therefore off. The
# connection string comes from PG_CONN_STR at run time, never from a file.
terraform {
  backend "pg" {
    skip_schema_creation = true
    skip_table_creation  = true
    skip_index_creation  = true
  }
}
