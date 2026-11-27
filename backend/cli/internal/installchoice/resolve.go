// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package installchoice resolves install inputs shared by Cobra and the wizard.
// Choices and Plan contain credentials and must never be logged or put in events.
package installchoice

import (
	"fmt"
	"os"

	"github.com/devicechain-io/dcctl/bootstrap"
)

// Presence records explicit input, independently of its value (including false/zero).
type Presence map[string]bool

func (p Presence) Has(name string) bool { return p[name] }

// Defaults supplies the same editable defaults as the production flag set.
func Defaults() Choices {
	c := Choices{Provider: "local", Cluster: bootstrap.DefaultClusterName, Given: Presence{}}
	for _, f := range c.flagValues() {
		c.Given[f.name] = false
	}
	return c
}

// Choices is the editable input. Given must be set when a screen edits a flag.
// Slice values retain input order; credentials remain private to resolution.
type Choices struct {
	Provider            string
	KubeContext         string
	Cluster             string
	DryRun              bool
	AssumeYes           bool
	SkipPreflight       bool
	Dev                 bool
	HA                  bool
	Compact             bool
	Size                string
	NoTLS               bool
	NoMonitoring        bool
	NoCNPG              bool
	AllowLegacyDb       bool
	BackupCredentials   string
	BackupSnapClass     string
	DBNodeSelector      []string
	DBTolerations       []string
	MaxConnections      int
	RelationalMaxWALMiB int
	RestoreRdbFrom      string
	RestoreRdbAt        string
	Registry            string
	ImageVersion        string
	Build               bool
	RemoveLegacy        bool
	AWSNLB              string
	MQTTGateway         bool
	Help                bool
	Alerts              Alerts
	Given               Presence
}

// Plan is resolved engine input plus the original choices for command reproduction.
type Plan struct {
	Options bootstrap.InstallOptions
	choices Choices
}

// Choices returns an isolated copy for a subsequent editing/review pass.
func (p Plan) Choices() Choices { return p.choices.Clone() }

// Prepared preserves the historical boundary: these validations happen before
// preflight; Resolve performs the later validations and alert-file reads after it.
type Prepared struct {
	Effective   Choices
	choices     Choices
	destination *bootstrap.BackupDestination
	restore     bootstrap.RestorePlan
	image       bootstrap.ImageSource
	version     string

	// Warnings contains display-safe deprecations from early input validation.
	Warnings []string
}

// Resolve resolves a screen's choices using exactly the CLI's validation path.
func (c Choices) Resolve(version string) (Plan, error) {
	p, err := c.Prepare(version, nil)
	if err != nil {
		return Plan{}, err
	}
	return p.Resolve()
}

func (p Prepared) Resolve() (Plan, error) {
	return p.ResolveWithReader(os.ReadFile)
}

// ResolveWithReader resolves the late options with the caller's alert-file reader.
// Install defers this call until its preflight succeeds or is skipped.
func (p Prepared) ResolveWithReader(readFile func(string) ([]byte, error)) (Plan, error) {
	opts, err := p.Effective.options(p.destination, p.restore, p.image, p.version, readFile)
	if err != nil {
		return Plan{}, err
	}
	return Plan{Options: opts, choices: p.choices}, nil
}

// Prepare settles presets and early refusals, retaining alert-file paths without
// reading them. It does not touch a cluster. Notice
// receives the legacy plain messages at their original positions; screens use nil.
func (c Choices) Prepare(version string, notice func(string)) (Prepared, error) {
	c = c.Clone()
	original := c.Clone()
	if notice == nil {
		notice = func(string) {}
	}
	compactOn, err := ResolveSize(c.Compact, c.Given.Has("compact"), c.Size)
	if err != nil {
		return Prepared{}, err
	}
	c.Compact = compactOn
	if c.Dev {
		res, err := ResolveDev(c.Given.Has, c.Build, c.ImageVersion)
		if err != nil {
			return Prepared{}, err
		}
		c.AssumeYes, c.Build = res.Yes, res.Build
		notice("dev mode: --build --yes")
	}
	// 🔴 --no-tls ALONE DOES NOTHING TO A CLUSTER, and accepting it would let an
	// operator believe cert-manager was left out.
	if c.Given.Has("no-tls") && c.NoTLS && !c.Compact {
		return Prepared{}, fmt.Errorf("--no-tls on dcctl install only takes effect with --compact (it drops " +
			"cert-manager and, with it, database backups). Without --compact, choose plain HTTP per " +
			"instance with dcctl bootstrap --no-tls")
	}
	if c.Compact {
		res := ResolveCompact(c.Given.Has, c.NoTLS, c.NoMonitoring)
		c.NoTLS, c.NoMonitoring = res.NoTLS, res.NoMonitoring
		notice(fmt.Sprintf("compact mode: %s", bootstrap.CompactSummary()))
	}
	if c.HA {
		notice(fmt.Sprintf("ha mode: %s", bootstrap.HaSummary(true)))
		if c.Compact {
			notice("note: --ha adds two more NATS servers per instance and their volumes; the printed compact footprint is measured single-node and is a floor, not the total")
		}
	}
	if err := bootstrap.ValidateRelationalStoreWAL(c.RelationalWAL()); err != nil {
		return Prepared{}, err
	}
	if c.MaxConnections != 0 && c.MaxConnections < 100 {
		return Prepared{}, fmt.Errorf("--max-connections must be at least 100 (got %d)", c.MaxConnections)
	}

	// Validated before any cluster exists: a bad object-store credential crashes
	// nothing. WAL archiving simply stops, and the first symptom is an archive-lag
	// alert — or a restore that finds no base backup.
	var warnings []string
	backupDestination, err := bootstrap.ParseBackupDestination(c.BackupCredentials, func(w string) {
		warnings = append(warnings, w)
	})
	if err != nil {
		return Prepared{}, fmt.Errorf("--backup-credentials-file: %w", err)
	}
	// 🔴 AN OFF-SITE ARCHIVE IS MEANINGLESS WITHOUT THE BACKUPS IT ARCHIVES, and the
	// flags that switch them off do so as a consequence of other choices — so this
	// combination is reachable without ever having asked for it.
	if backupDestination.Configured() &&
		!bootstrap.DatabaseBackupsEnabled(c.NoCNPG, c.Compact, c.NoTLS) {
		return Prepared{}, fmt.Errorf("--backup-credentials-file names an off-site archive, but this " +
			"combination of flags leaves the cluster with no database backups to send there " +
			"(--no-cnpg removes the operator the backup plugin extends; --compact with " +
			"--no-tls drops the cert-manager it needs). Drop the flag, or drop whichever of " +
			"those turned backups off")
	}

	// Volume-snapshot base backups: a name no object can have, or snapshots on a
	// cluster whose other flags turned its backups off, are both knowable from argv.
	// Whether the class exists, deletes what it snapshots and belongs to the right
	// driver needs the cluster, and the install asks it before writing anything.
	if err := bootstrap.ValidateBackupSnapshotClass(c.BackupSnapClass,
		bootstrap.DatabaseBackupsEnabled(c.NoCNPG, c.Compact, c.NoTLS)); err != nil {
		return Prepared{}, err
	}

	// Placement: the syntax, and a toleration with nothing to place, are knowable from
	// argv. Whether the nodes can take the databases needs the cluster, and the
	// install asks it before writing anything.
	// Options reads the same choices again when it builds the engine's options;
	// this call only moves the refusal ahead of the cluster checks below.
	if _, err := c.Placement(); err != nil {
		return Prepared{}, err
	}

	// 🔴 SETTLED FROM ARGV, BEFORE ANY CLUSTER IS TOUCHED. Every way a restore's
	// flags can be wrong — a recovery target with nothing to recover, a timestamp
	// with no offset, an archive on a cluster that has no plugin to read it — is
	// knowable here, and finding out ten minutes into a rebuild, during an
	// incident, is the expensive time to find out.
	restorePlan, err := bootstrap.ResolveRestorePlan(c.RestoreFlags(
		bootstrap.DatabaseBackupsEnabled(c.NoCNPG, c.Compact, c.NoTLS)))
	if err != nil {
		return Prepared{}, err
	}

	// 🔴 SETTLED FROM ARGV TOO, AND FOR A REASON THIS COMMAND ONLY ACQUIRED WHEN
	// IT TOOK OVER THE OPERATOR. `dcctl install` deploys a workload now — the
	// controller Deployment names an image — so the failure ResolveImageSource
	// exists to catch reaches this verb: a dcctl built from source carries the
	// unpublished tag "dev", which names no image in any registry and manifests
	// as an ImagePullBackOff on a controller nobody is watching, minutes after a
	// cluster was created to hold it.
	imageSource, err := bootstrap.ResolveImageSource(c.Registry, c.ImageVersion, c.Build)
	if err != nil {
		return Prepared{}, err
	}
	// The recorded install is what `dcctl upgrade` trusts to say which controller the cluster
	// runs, so it must not name this dcctl for an older one. Only an explicit --version.
	if c.ImageVersion != "" && !c.Build {
		if err := bootstrap.RefuseForeignRelease(imageSource.Version, imageSource.Registry); err != nil {
			return Prepared{}, err
		}
	}
	return Prepared{Effective: c, choices: original, Warnings: warnings, destination: backupDestination, restore: restorePlan, image: imageSource, version: version}, nil
}

// BootstrapOptions supplies the run's target and request before preflight.
// It uses the effective presets and contains no alert-file reads.
func (p Prepared) BootstrapOptions() bootstrap.Options {
	return p.Effective.bootstrapOptions(p.image)
}

func (c Choices) bootstrapOptions(img bootstrap.ImageSource) bootstrap.Options {
	return bootstrap.Options{
		KubeContext:          c.KubeContext,
		Cluster:              c.Cluster,
		DryRun:               c.DryRun,
		AssumeYes:            c.AssumeYes,
		NoTLS:                c.NoTLS,
		AllowLegacyDbRemoval: c.AllowLegacyDb,
		ImageRegistry:        img.Registry,
		ImageVersion:         img.Version,
		BuildImages:          c.Build,
	}
}

// Options assembles the engine input from settled early plans.
func (c Choices) Options(dest *bootstrap.BackupDestination, restore bootstrap.RestorePlan,
	img bootstrap.ImageSource, version string) (bootstrap.InstallOptions, error) {
	return c.options(dest, restore, img, version, os.ReadFile)
}

func (c Choices) options(dest *bootstrap.BackupDestination, restore bootstrap.RestorePlan,
	img bootstrap.ImageSource, version string, readFile func(string) ([]byte, error)) (bootstrap.InstallOptions, error) {
	if err := bootstrap.ValidateAWSNLB(c.AWSNLB); err != nil {
		return bootstrap.InstallOptions{}, err
	}
	wal := c.RelationalWAL()
	if err := bootstrap.ValidateRelationalStoreWAL(wal); err != nil {
		return bootstrap.InstallOptions{}, err
	}
	placement, err := c.Placement()
	if err != nil {
		return bootstrap.InstallOptions{}, err
	}
	alerts, err := c.Alerts.RequestWithReader(readFile)
	if err != nil {
		return bootstrap.InstallOptions{}, err
	}
	if alerts.Given() && c.NoMonitoring {
		return bootstrap.InstallOptions{}, fmt.Errorf("the --alert-* flags route the monitoring stack's alerts, and this install " +
			"runs none (--no-monitoring, or --compact, which leaves it out unless --no-monitoring=false). Drop them, or install the monitoring stack")
	}
	return bootstrap.InstallOptions{
		Options:           c.bootstrapOptions(img),
		NoMonitoring:      c.NoMonitoring,
		NoCNPG:            c.NoCNPG,
		Compact:           c.Compact,
		HA:                c.HA,
		BackupDestination: dest,
		// Checked from argv by ValidateBackupSnapshotClass, and against the cluster by
		// the install itself before anything is written.
		BackupSnapshotClass: c.BackupSnapClass,
		// Parsed from argv above by ParseDatabasePlacement, and checked against the cluster's
		// nodes by the install itself before anything is written.
		DatabasePlacement:        placement,
		MaxConnections:           c.MaxConnections,
		RelationalStoreMaxWALMiB: wal,
		Restore:                  restore,
		DcctlVersion:             version,

		RemoveLegacyIngress: c.RemoveLegacy,
		AWSNLB:              c.AWSNLB,
		MQTTGateway:         optionalBool(c.Given.Has("mqtt-gateway"), c.MQTTGateway),
		AlertRouting:        alerts,
	}, nil
}

func (c Choices) RelationalWAL() *int {
	if !c.Given.Has("relational-store-max-wal-mib") {
		return nil
	}
	v := c.RelationalMaxWALMiB
	return &v
}

func optionalBool(given, value bool) *bool {
	if !given {
		return nil
	}
	return &value
}

func (c Choices) Placement() (bootstrap.DatabasePlacement, error) {
	return bootstrap.ParseDatabasePlacement(c.DBNodeSelector, c.DBTolerations)
}

func (c Choices) RestoreFlags(enabled bool) bootstrap.RestoreFlags {
	return bootstrap.RestoreFlags{RdbFrom: c.RestoreRdbFrom, RdbTargetTime: c.RestoreRdbAt, BackupsEnabled: enabled}
}

func ResolveDev(changed func(string) bool, build bool, version string) (installDevResolution, error) {
	if changed("build") && !build {
		return installDevResolution{}, fmt.Errorf(
			"--dev builds the operator image from source; remove --build=false (or drop --dev)")
	}
	if changed("version") {
		return installDevResolution{}, fmt.Errorf(
			"--dev builds the operator image from source and tags it \"dev\", so "+
				"--version %s cannot also apply; drop one of them", version)
	}
	return installDevResolution{Build: true, Yes: true}, nil
}

func ResolveCompact(changed func(string) bool, noTLS, noMonitoring bool) compactModeResolution {
	res := compactModeResolution{NoTLS: true, NoMonitoring: true}
	// An explicit --no-tls=false keeps TLS (and therefore cert-manager); an explicit
	// --no-monitoring=false keeps the observability stack. Both cost footprint, and
	// both are the operator's call to make.
	if changed("no-tls") {
		res.NoTLS = noTLS
	}
	if changed("no-monitoring") {
		res.NoMonitoring = noMonitoring
	}
	return res
}

type installDevResolution struct {
	Build bool
	Yes   bool
}

type compactModeResolution struct {
	NoTLS        bool
	NoMonitoring bool
}

func ResolveSize(compact, compactGiven bool, size string) (bool, error) {
	if err := bootstrap.ValidateSize(size); err != nil {
		return false, fmt.Errorf("--size: %w", err)
	}
	switch size {
	case "", bootstrap.SizeBase:
		if compact && size == bootstrap.SizeBase {
			return false, fmt.Errorf("--compact and --size base contradict each other")
		}
		return compact, nil
	case bootstrap.SizeCompact:
		if compactGiven && !compact {
			return false, fmt.Errorf("--compact=false and --size compact contradict each other")
		}
		return true, nil
	default:
		return false, fmt.Errorf("--size %s is chosen per instance: run `dcctl bootstrap --size %s`. "+
			"dcctl install takes %s or %s", size, size, bootstrap.SizeBase, bootstrap.SizeCompact)
	}
}
