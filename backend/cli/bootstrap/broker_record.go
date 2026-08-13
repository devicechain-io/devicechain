// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// The broker credential record bridges a gap in the bootstrap's own ordering.
//
// 🔴 THE BROKER IS CONFIGURED IN STEP 3; THE FIRST DURABLE COPY OF ITS CREDENTIALS IS
// WRITTEN IN STEP 5. A run that dies in between leaves a live broker holding credentials
// that no later run can reuse, because the only place dcctl looks for them — the instance
// chart's config Secret — does not exist yet. Each retry mints fresh ones.
//
// It cannot be fixed by reading the cluster harder. Step 3 receives the callout issuer's
// PUBLIC key and two BCRYPT hashes and nothing else; the ConfigMap it renders carries
// hashes; the OpenTofu state carries hashes, the public key and TLS material. Neither a
// public nkey nor a bcrypt hash can be inverted, so DeployedBrokerHashes can only ever
// return hashes — useless without the plaintexts they verify. Before step 5 the seed and
// both plaintexts exist only in this process's memory. They have to be written down before
// step 3 runs, or they are gone. (broker_record_test.go asserts that premise still holds,
// because it is a property of what infraVars passes and nothing else enforces it.)
//
// WHY LOCAL DISK AND NOT A CLUSTER SECRET
//
// A Secret in dc-system is the instinct. On a fresh install that namespace does not exist
// until OpenTofu creates it in step 3; dcctl could create it first and turn the module's
// toggle off, but flipping that toggle on an EXISTING instance moves a count from 1 to 0
// and plans destruction of dc-system — cascading the broker, both databases and the object
// store. Writing the Secret after the apply does not close the window either, since dying
// mid-apply is the observed failure.
//
// The deciding argument is that this file sits beside the OpenTofu state, and that state is
// local-disk-only: there is no backend block anywhere, so a re-run from a different machine
// already collides on every resource. A cluster Secret would be MORE portable than the state
// it accompanies, which is a promise the rest of the command cannot keep. The one realistic
// cross-machine recipe — copying ~/.devicechain/<instance>/ wholesale — carries this record
// with it.
//
// The directory's threat model is unchanged by this file: the tfstate next to it already
// holds the broker's TLS private key and the database superuser password in cleartext, and
// both are 0700/0600 with an explicit chmod walk for trees an older dcctl created.

// brokerRecordFile is the record's name inside ~/.devicechain/<instance>/.
//
// 🔴 IT MUST NOT MATCH looksLikeEscrow. A full `dcctl destroy` removes the instance
// directory but SPARES anything whose name contains ".escrow", "rootkey" or "root-key" —
// so a record named to sit "next to the escrow" would survive a teardown, and a same-name
// rebuild would then find no instance config, a present record, and silently resurrect the
// destroyed instance's broker credentials. That is credential inheritance across
// generations, and the reuse log line would report it as a truthful reuse.
const brokerRecordFile = "broker-credentials.json"

// brokerRecord is the material the cluster cannot yield after step 3.
//
// The bcrypt hashes are deliberately absent: they are derived from these plaintexts,
// DeployedBrokerHashes already reads them back from the broker itself, and a second copy is
// a second thing that can disagree. The shape leaves room for the secret-store root key to
// join later — it has the same window under --no-escrow — without a format change.
type brokerRecord struct {
	Instance        string    `json:"instance"`
	Written         time.Time `json:"written"`
	IssuerSeed      string    `json:"issuerSeed"`
	ServicePassword string    `json:"servicePassword"`
	SysPassword     string    `json:"sysPassword"`
}

// brokerRecordPathFor resolves the record's path. A seam so tests never touch a real
// instance's directory: every credential test runs with Instance "prod", and on a
// maintainer's machine ~/.devicechain/prod holds live state.
var brokerRecordPathFor = func(instance string) (string, error) {
	root, err := instanceRoot(instance)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, brokerRecordFile), nil
}

// readBrokerRecord returns the recorded credentials for instance, or nil.
//
// 🔴 IT CANNOT FAIL A RUN, AND THAT IS THE CONSTRAINT THE WHOLE READ IS BUILT UNDER.
// Missing, unreadable, truncated, malformed, or written for a different instance all mean
// the same thing: no record, mint as before. This is the standing objection that
// DeployedBrokerHashes was written to answer — a read added for a rare recovery must not
// give an ORDINARY re-run a new way to fail — and it applies here unchanged. The worst case
// is exactly today's behaviour.
//
// The instance mismatch is deliberately in that list rather than being an error. It should
// be unreachable, and minting is the safe answer; refusing to run is not.
func readBrokerRecord(instance string) *brokerRecord {
	path, err := brokerRecordPathFor(instance)
	if err != nil {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var rec brokerRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil
	}
	if rec.Instance != instance || rec.IssuerSeed == "" || rec.ServicePassword == "" {
		return nil
	}
	return &rec
}

// writeBrokerRecord stores the record for instance, replacing any existing one.
//
// 🔴 WRITTEN WHOLE OR NOT AT ALL. A crash partway through a plain write leaves a truncated
// file, readBrokerRecord treats it as absent, and the retry that most needs the record
// mints instead — the observed defect, reintroduced by its own fix. Temp file in the same
// directory, explicit chmod, fsync, then rename.
//
// The explicit chmod is not belt-and-braces: os.WriteFile's mode applies only when it
// CREATES the file, so a rewrite over an existing record would inherit whatever mode that
// one had. Creating the temp under a fresh name each time and chmod-ing it makes the mode a
// property of this write rather than of the file's history.
//
// Two concurrent dcctl runs against one instance are out of scope — nothing else in the
// pipeline guards against that either, and OpenTofu's own state lock is the only thing that
// would notice.
func writeBrokerRecord(instance string, rec brokerRecord) error {
	dir, err := instanceStateDir(instance, "")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, brokerRecordFile)

	raw, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, brokerRecordFile+".*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once the rename succeeds

	if err := tmp.Chmod(stateFileMode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("storing the broker credential record: %w", err)
	}
	return nil
}
