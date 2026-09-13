// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// backupCredentialsSecretName is the Secret the backup plugin presents to an
// EXTERNAL object store. Named by the infrastructure tree, which references it as a
// string, so this is a contract rather than a detail.
const backupCredentialsSecretName = "dc-backup-credentials"

// The key names inside it, read by the CNPG-I barman plugin's ObjectStore through
// s3Credentials.accessKeyId.key / secretAccessKey.key.
const (
	keyBackupAccessKeyID     = "ACCESS_KEY_ID"
	keyBackupSecretAccessKey = "SECRET_ACCESS_KEY"
)

// BackupDestination is an off-site archive an operator already owns, described by
// --backup-credentials-file.
//
// 🔴 SUPPLIED, NEVER MINTED, AND THAT IS WHAT MAKES IT A DIFFERENT SHAPE FROM
// EVERYTHING ELSE dcctl WRITES. Every other credential in this package is entropy
// this process generates, so the only question is where it lands. These belong to
// somebody else's object store: dcctl cannot invent them, cannot rotate them, and
// cannot check them beyond their shape. What it CAN do is refuse a combination that
// would archive nowhere, and do it before a cluster exists.
//
// The whole destination travels in one file rather than the credentials alone. The
// alternative was credentials here and endpoint/buckets as OpenTofu variables, which
// is how it worked before this and reads as incoherent from the operator's side —
// "put your key in a file and the bucket it opens in an environment variable". One
// file answers one question: where do backups go, and how do we authenticate to it.
type BackupDestination struct {
	// EndpointURL is the S3-compatible endpoint, e.g. https://s3.us-east-1.amazonaws.com
	// or a MinIO/Ceph address.
	EndpointURL string `json:"endpointUrl"`
	// BucketRdb and BucketTsdb are the two archives. They are separate so the event
	// store can be restored without touching the control plane, and vice versa.
	BucketRdb  string `json:"bucketRdb"`
	BucketTsdb string `json:"bucketTsdb"`
	// AccessKeyID and SecretAccessKey are what the archiver presents.
	AccessKeyID     string `json:"accessKeyId"`
	SecretAccessKey string `json:"secretAccessKey"`
}

// Configured reports whether a destination was supplied at all.
func (b *BackupDestination) Configured() bool { return b != nil && b.EndpointURL != "" }

// ParseBackupDestination reads and validates --backup-credentials-file.
//
// Validated UP FRONT, before a cluster exists, for the same reason
// ParseLwm2mIdentities is: every way this can be wrong is knowable from the file
// alone, and the alternative is finding out when the first base backup fails —
// which is silent. WAL archiving that cannot authenticate does not crash the
// database; it just stops shipping, and the first symptom is an archive-lag alert
// nobody has wired to a pager yet.
//
// An empty path returns (nil, nil): no external destination, use the in-cluster one.
func ParseBackupDestination(path string) (*BackupDestination, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading backup credentials file: %w", err)
	}

	var d BackupDestination
	dec := json.NewDecoder(bytes.NewReader(raw))
	// A misspelled key is a field that silently keeps its zero value, and for
	// secretAccessKey that is an archiver presenting an empty credential.
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("parsing backup credentials file (expected a JSON object of "+
			"{endpointUrl, bucketRdb, bucketTsdb, accessKeyId, secretAccessKey}): %w", err)
	}
	if dec.More() {
		return nil, fmt.Errorf("backup credentials file %q has trailing content after the JSON object", path)
	}

	blank := func(s string) bool { return strings.TrimSpace(s) == "" }
	for _, f := range []struct{ name, value string }{
		{"endpointUrl", d.EndpointURL},
		{"bucketRdb", d.BucketRdb},
		{"bucketTsdb", d.BucketTsdb},
		{"accessKeyId", d.AccessKeyID},
		{"secretAccessKey", d.SecretAccessKey},
	} {
		if blank(f.value) {
			return nil, fmt.Errorf("%s is required — an external backup destination with no %s "+
				"archives nowhere, and a database whose WAL is not being shipped looks perfectly "+
				"healthy right up until it has to be restored", f.name, f.name)
		}
	}

	u, err := url.Parse(d.EndpointURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("endpointUrl %q is not an http(s) URL with a host; the archiver "+
			"dials this address and a malformed one fails at the first base backup rather than here",
			d.EndpointURL)
	}

	// 🔴 THE TWO BUCKETS MUST DIFFER. One bucket for both stores means each archives
	// over the other's path prefix, and the failure is not visible until a restore
	// reads a base backup belonging to the wrong database. The root refuses this too;
	// refusing here costs the operator a line instead of a rebuild.
	if d.BucketRdb == d.BucketTsdb {
		return nil, fmt.Errorf("bucketRdb and bucketTsdb are both %q — the two stores keep "+
			"independent timelines and must archive to separate buckets, or restoring one "+
			"reads the other's base backup", d.BucketRdb)
	}

	// The object store's own floor, applied to a SUPPLIED value for the first time.
	// The in-cluster path checks this at mint; nothing checked it here, and the
	// OpenTofu variable validation that used to is being retired with the variable.
	if err := validateObjectStoreSecret(d.SecretAccessKey); err != nil {
		return nil, fmt.Errorf("secretAccessKey: %w", err)
	}
	return &d, nil
}

// backupCredentialsSecret is the Secret the archiver reads.
//
// 🔑 dcctl OWNS IT EVEN THOUGH IT DID NOT MINT IT. The ownership annotations are
// about who WRITES the object, not where the value came from — without them
// writeOwnedSecret would refuse to update the Secret on the next run, and an
// operator rotating their own S3 key by editing the file would be told the Secret
// belongs to somebody else.
func backupCredentialsSecret(d *BackupDestination) ownedSecret {
	return ownedSecret{
		Name:      backupCredentialsSecretName,
		Namespace: infraNamespace,
		Type:      corev1.SecretTypeOpaque,
		Labels: map[string]string{
			"app.kubernetes.io/name":      "dc-backup",
			"app.kubernetes.io/component": "database-backup",
		},
		Data: map[string]string{
			keyBackupAccessKeyID:     d.AccessKeyID,
			keyBackupSecretAccessKey: d.SecretAccessKey,
		},
	}
}
