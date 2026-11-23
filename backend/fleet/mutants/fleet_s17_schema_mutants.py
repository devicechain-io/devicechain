#!/usr/bin/env python3
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""Mutants for the fleet schema migration (harness name fleet-s17). Each is ONE exact edit to the
migration; the named tests under the fleetpg tag must then FAIL. The baseline runs first as a no-op
control and aborts the run if red. The file is restored from a byte copy. Needs DC_FLEET_PG_DSN.

    cd backend/fleet && python3 mutants/fleet_s17_schema_mutants.py
"""
import os, subprocess, sys
ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
F = ROOT + '/schema/migration_writer_exclusion.go'
MUT = [
  ('drop-a-check', '\t\t{"writer_slots", "ck_writer_slots_excluded_both_cuts_v1",\n', '\t\t{"writer_slots", "ck_writer_slots_excluded_both_cuts_v1_dropped_",\n', 'TestFleetPGEveryCheck|TestFleetPGLegalRows'),
  ('drop-immutability-trigger', '\t`CREATE TRIGGER tr_cloud_identities_immutable BEFORE UPDATE ON cloud_identities FOR EACH ROW EXECUTE FUNCTION cloud_identities_immutable()`,\n', '', 'TestFleetPGIdentityIsImmutable|TestFleetPGReplay'),
  ('drop-append-only-trigger', '\t`CREATE TRIGGER tr_writer_slot_transitions_append_only BEFORE UPDATE OR DELETE ON writer_slot_transitions FOR EACH ROW EXECUTE FUNCTION writer_slot_transitions_append_only()`,\n', '', 'TestFleetPGTransitionsAreAppendOnly'),
  ('drop-the-nonce-unique-index', '\t`CREATE UNIQUE INDEX IF NOT EXISTS ux_cloud_identities_ownership_nonce ON cloud_identities (ownership_nonce) WHERE ownership_nonce IS NOT NULL`,\n', '', 'TestFleetPGUniqueIndexes|TestFleetPGReplay'),
  ('drop-the-imported-address-index', '\t`CREATE UNIQUE INDEX IF NOT EXISTS ux_import_records_imported_address ON import_records (cluster_uid, address) WHERE decision = \'IMPORTED\'`,\n', '', 'TestFleetPGUniqueIndexes'),
  ('drop-a-foreign-key', '\t\t{"runs", "fk_runs_cluster_uid", "cluster_uid", "cloud_identities", "cluster_uid"},\n', '', 'TestFleetPGForeignKeys|TestFleetPGReplay'),
  ('weaken-the-empty-resource-id-check', "`cloud_resource_id IS NULL OR cloud_resource_id <> ''`", "`cloud_resource_id IS NULL OR cloud_resource_id = cloud_resource_id`", 'TestFleetPGEveryCheck'),
  ('seed-the-lease-row', '\t\t\treturn applyWriterExclusionDDL(tx)\n', '\t\t\tif err := applyWriterExclusionDDL(tx); err != nil {\n\t\t\t\treturn err\n\t\t\t}\n\t\t\treturn tx.Exec(`INSERT INTO manager_lease (id, holder_id, expires_at, updated_at) VALUES (1, \'seed\', now(), now()) ON CONFLICT DO NOTHING`).Error\n', 'TestFleetPGMigrationAppliesAndReapplies'),
]
env = dict(os.environ)
def run(rx):
    r = subprocess.run(['go','test','-tags','fleetpg','-count=1','-p','1','-run',rx,'./schema'],cwd=ROOT,env=env,capture_output=True,text=True)
    return r.returncode, r.stdout+r.stderr
rc,out = run('.')
if rc: sys.exit('ABORT baseline red\n'+out[-1500:])
print('baseline green')
orig = open(F,'rb').read()
bad = 0
for name,old,new,rx in MUT:
    t = orig.decode('utf-8')
    if t.count(old)!=1:
        print('BAD', name, t.count(old)); bad+=1; continue
    try:
        open(F,'wb').write(t.replace(old,new,1).encode('utf-8'))
        rc,out = run(rx)
    finally:
        open(F,'wb').write(orig)
    if rc==0: print('SURVIVED',name); bad+=1
    elif 'build failed' in out or '[build failed]' in out or 'no required module' in out or 'cannot find' in out: print('BUILD',name, out[-300:].replace('\n',' ')); bad+=1
    else: print('killed  ',name)
# a snapshot that imports a live model: needs a package to import, so one is planted and removed
os.makedirs(ROOT + '/model', exist_ok=True)
open(ROOT + '/model/model.go', 'w').write('package model'+chr(10))
open(ROOT + '/schema/zz_mutant.go', 'w').write('package schema'+chr(10)+chr(10)+'import _ "github.com/devicechain-io/dc-fleet/model"'+chr(10))
try:
    rc,out = run('TestMigrationsImportNoLiveModels')
finally:
    os.remove(ROOT + '/schema/zz_mutant.go'); os.remove(ROOT + '/model/model.go'); os.rmdir(ROOT + '/model')
print('killed   snapshot-imports-a-live-model' if rc else 'SURVIVED snapshot-imports-a-live-model')
bad += (rc == 0)
sys.exit(1 if bad else 0)
