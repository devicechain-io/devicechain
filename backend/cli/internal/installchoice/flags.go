// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installchoice

import (
	"fmt"
	"strconv"

	"github.com/spf13/pflag"
)

// PresentationFlags are the install flags that choose how a run is shown, never
// what it does. They are not choices: no plan, option or equivalent command
// carries them.
func PresentationFlags() []string { return []string{"ui", "no-splash", "output"} }

// FromFlags snapshots the real parsed Cobra flags, including every Changed bit.
// No input aliases or preset expansion are lost at this boundary.
func FromFlags(fs *pflag.FlagSet) (Choices, error) {
	known := map[string]bool{}
	for _, name := range PresentationFlags() {
		known[name] = true
	}
	for _, f := range (Choices{}).flagValues() {
		known[f.name] = true
	}
	var unknown bool
	fs.VisitAll(func(f *pflag.Flag) {
		if !known[f.Name] {
			unknown = true
		}
	})
	if unknown {
		return Choices{}, fmt.Errorf("install choice schema does not cover the flag set")
	}
	var c Choices
	c.Given = Presence{}
	var err error
	c.Alerts.Config, err = fs.GetString("alert-config")
	if err != nil {
		return Choices{}, err
	}
	c.Given["alert-config"] = fs.Changed("alert-config")
	c.Alerts.Email, err = sliceValue(fs, "alert-email")
	if err != nil {
		return Choices{}, err
	}
	c.Given["alert-email"] = fs.Changed("alert-email")
	c.Alerts.SMTPFrom, err = fs.GetString("alert-smtp-from")
	if err != nil {
		return Choices{}, err
	}
	c.Given["alert-smtp-from"] = fs.Changed("alert-smtp-from")
	c.Alerts.SMTPHost, err = fs.GetString("alert-smtp-host")
	if err != nil {
		return Choices{}, err
	}
	c.Given["alert-smtp-host"] = fs.Changed("alert-smtp-host")
	c.Alerts.SMTPPassFile, err = fs.GetString("alert-smtp-password-file")
	if err != nil {
		return Choices{}, err
	}
	c.Given["alert-smtp-password-file"] = fs.Changed("alert-smtp-password-file")
	c.Alerts.SMTPUser, err = fs.GetString("alert-smtp-user")
	if err != nil {
		return Choices{}, err
	}
	c.Given["alert-smtp-user"] = fs.Changed("alert-smtp-user")
	c.Alerts.Watchdog, err = fs.GetString("alert-watchdog-webhook")
	if err != nil {
		return Choices{}, err
	}
	c.Given["alert-watchdog-webhook"] = fs.Changed("alert-watchdog-webhook")
	c.Alerts.Webhook, err = fs.GetString("alert-webhook")
	if err != nil {
		return Choices{}, err
	}
	c.Given["alert-webhook"] = fs.Changed("alert-webhook")
	c.AllowLegacyDb, err = fs.GetBool("allow-legacy-db-removal")
	if err != nil {
		return Choices{}, err
	}
	c.Given["allow-legacy-db-removal"] = fs.Changed("allow-legacy-db-removal")
	c.AWSNLB, err = fs.GetString("aws-nlb")
	if err != nil {
		return Choices{}, err
	}
	c.Given["aws-nlb"] = fs.Changed("aws-nlb")
	c.BackupCredentials, err = fs.GetString("backup-credentials-file")
	if err != nil {
		return Choices{}, err
	}
	c.Given["backup-credentials-file"] = fs.Changed("backup-credentials-file")
	c.BackupSnapClass, err = fs.GetString("backup-snapshot-class")
	if err != nil {
		return Choices{}, err
	}
	c.Given["backup-snapshot-class"] = fs.Changed("backup-snapshot-class")
	c.Build, err = fs.GetBool("build")
	if err != nil {
		return Choices{}, err
	}
	c.Given["build"] = fs.Changed("build")
	c.Alerts.Clear, err = fs.GetBool("clear-alert-routing")
	if err != nil {
		return Choices{}, err
	}
	c.Given["clear-alert-routing"] = fs.Changed("clear-alert-routing")
	c.Cluster, err = fs.GetString("cluster")
	if err != nil {
		return Choices{}, err
	}
	c.Given["cluster"] = fs.Changed("cluster")
	c.Compact, err = fs.GetBool("compact")
	if err != nil {
		return Choices{}, err
	}
	c.Given["compact"] = fs.Changed("compact")
	c.DBNodeSelector, err = sliceValue(fs, "database-node-selector")
	if err != nil {
		return Choices{}, err
	}
	c.Given["database-node-selector"] = fs.Changed("database-node-selector")
	c.DBTolerations, err = sliceValue(fs, "database-toleration")
	if err != nil {
		return Choices{}, err
	}
	c.Given["database-toleration"] = fs.Changed("database-toleration")
	c.Dev, err = fs.GetBool("dev")
	if err != nil {
		return Choices{}, err
	}
	c.Given["dev"] = fs.Changed("dev")
	c.DryRun, err = fs.GetBool("dry-run")
	if err != nil {
		return Choices{}, err
	}
	c.Given["dry-run"] = fs.Changed("dry-run")
	c.HA, err = fs.GetBool("ha")
	if err != nil {
		return Choices{}, err
	}
	c.Given["ha"] = fs.Changed("ha")
	c.Help, err = fs.GetBool("help")
	if err != nil {
		return Choices{}, err
	}
	c.Given["help"] = fs.Changed("help")
	c.KubeContext, err = fs.GetString("kube-context")
	if err != nil {
		return Choices{}, err
	}
	c.Given["kube-context"] = fs.Changed("kube-context")
	c.MaxConnections, err = fs.GetInt("max-connections")
	if err != nil {
		return Choices{}, err
	}
	c.Given["max-connections"] = fs.Changed("max-connections")
	c.MQTTGateway, err = fs.GetBool("mqtt-gateway")
	if err != nil {
		return Choices{}, err
	}
	c.Given["mqtt-gateway"] = fs.Changed("mqtt-gateway")
	c.NoCNPG, err = fs.GetBool("no-cnpg")
	if err != nil {
		return Choices{}, err
	}
	c.Given["no-cnpg"] = fs.Changed("no-cnpg")
	c.NoMonitoring, err = fs.GetBool("no-monitoring")
	if err != nil {
		return Choices{}, err
	}
	c.Given["no-monitoring"] = fs.Changed("no-monitoring")
	c.NoTLS, err = fs.GetBool("no-tls")
	if err != nil {
		return Choices{}, err
	}
	c.Given["no-tls"] = fs.Changed("no-tls")
	c.Registry, err = fs.GetString("registry")
	if err != nil {
		return Choices{}, err
	}
	c.Given["registry"] = fs.Changed("registry")
	c.RelationalMaxWALMiB, err = fs.GetInt("relational-store-max-wal-mib")
	if err != nil {
		return Choices{}, err
	}
	c.Given["relational-store-max-wal-mib"] = fs.Changed("relational-store-max-wal-mib")
	c.RemoveLegacy, err = fs.GetBool("remove-legacy-ingress")
	if err != nil {
		return Choices{}, err
	}
	c.Given["remove-legacy-ingress"] = fs.Changed("remove-legacy-ingress")
	c.RestoreRdbAt, err = fs.GetString("restore-rdb-at")
	if err != nil {
		return Choices{}, err
	}
	c.Given["restore-rdb-at"] = fs.Changed("restore-rdb-at")
	c.RestoreRdbFrom, err = fs.GetString("restore-rdb-from")
	if err != nil {
		return Choices{}, err
	}
	c.Given["restore-rdb-from"] = fs.Changed("restore-rdb-from")
	c.Size, err = fs.GetString("size")
	if err != nil {
		return Choices{}, err
	}
	c.Given["size"] = fs.Changed("size")
	c.SkipPreflight, err = fs.GetBool("skip-preflight")
	if err != nil {
		return Choices{}, err
	}
	c.Given["skip-preflight"] = fs.Changed("skip-preflight")
	c.ImageVersion, err = fs.GetString("version")
	if err != nil {
		return Choices{}, err
	}
	c.Given["version"] = fs.Changed("version")
	c.AssumeYes, err = fs.GetBool("yes")
	if err != nil {
		return Choices{}, err
	}
	c.Given["yes"] = fs.Changed("yes")
	return c.Clone(), nil
}

// Clone isolates editable screens and plans from subsequent Cobra parses.
func (c Choices) Clone() Choices {
	p := Presence{}
	for k, v := range c.Given {
		p[k] = v
	}
	c.Given = p
	if c.DBNodeSelector != nil {
		c.DBNodeSelector = append([]string{}, c.DBNodeSelector...)
	}
	if c.DBTolerations != nil {
		c.DBTolerations = append([]string{}, c.DBTolerations...)
	}
	if c.Alerts.Email != nil {
		c.Alerts.Email = append([]string{}, c.Alerts.Email...)
	}
	return c
}

type flagValue struct {
	name   string
	values []string
	slice  bool
}

// flagValues has a stable alphabetical flag order, with repeated values in
// their original order. The spec constrains repeat order, not cross-flag order.
func (c Choices) flagValues() []flagValue {
	return []flagValue{
		{"alert-config", []string{c.Alerts.Config}, false},
		{"alert-email", c.Alerts.Email, true},
		{"alert-smtp-from", []string{c.Alerts.SMTPFrom}, false},
		{"alert-smtp-host", []string{c.Alerts.SMTPHost}, false},
		{"alert-smtp-password-file", []string{c.Alerts.SMTPPassFile}, false},
		{"alert-smtp-user", []string{c.Alerts.SMTPUser}, false},
		{"alert-watchdog-webhook", []string{c.Alerts.Watchdog}, false},
		{"alert-webhook", []string{c.Alerts.Webhook}, false},
		{"allow-legacy-db-removal", []string{strconv.FormatBool(c.AllowLegacyDb)}, false},
		{"aws-nlb", []string{c.AWSNLB}, false},
		{"backup-credentials-file", []string{c.BackupCredentials}, false},
		{"backup-snapshot-class", []string{c.BackupSnapClass}, false},
		{"build", []string{strconv.FormatBool(c.Build)}, false},
		{"clear-alert-routing", []string{strconv.FormatBool(c.Alerts.Clear)}, false},
		{"cluster", []string{c.Cluster}, false},
		{"compact", []string{strconv.FormatBool(c.Compact)}, false},
		{"database-node-selector", c.DBNodeSelector, true},
		{"database-toleration", c.DBTolerations, true},
		{"dev", []string{strconv.FormatBool(c.Dev)}, false},
		{"dry-run", []string{strconv.FormatBool(c.DryRun)}, false},
		{"ha", []string{strconv.FormatBool(c.HA)}, false},
		{"help", []string{strconv.FormatBool(c.Help)}, false},
		{"kube-context", []string{c.KubeContext}, false},
		{"max-connections", []string{strconv.Itoa(c.MaxConnections)}, false},
		{"mqtt-gateway", []string{strconv.FormatBool(c.MQTTGateway)}, false},
		{"no-cnpg", []string{strconv.FormatBool(c.NoCNPG)}, false},
		{"no-monitoring", []string{strconv.FormatBool(c.NoMonitoring)}, false},
		{"no-tls", []string{strconv.FormatBool(c.NoTLS)}, false},
		{"registry", []string{c.Registry}, false},
		{"relational-store-max-wal-mib", []string{strconv.Itoa(c.RelationalMaxWALMiB)}, false},
		{"remove-legacy-ingress", []string{strconv.FormatBool(c.RemoveLegacy)}, false},
		{"restore-rdb-at", []string{c.RestoreRdbAt}, false},
		{"restore-rdb-from", []string{c.RestoreRdbFrom}, false},
		{"size", []string{c.Size}, false},
		{"skip-preflight", []string{strconv.FormatBool(c.SkipPreflight)}, false},
		{"version", []string{c.ImageVersion}, false},
		{"yes", []string{strconv.FormatBool(c.AssumeYes)}, false},
	}
}

// GetStringSlice reparses Value.String() and converts a nil default to [].
// Read the typed value to preserve the engine-options JSON's null/[] distinction.
func sliceValue(fs *pflag.FlagSet, name string) ([]string, error) {
	f := fs.Lookup(name)
	if f == nil {
		return nil, fmt.Errorf("missing install slice flag")
	}
	v, ok := f.Value.(pflag.SliceValue)
	if !ok {
		return nil, fmt.Errorf("unexpected install slice flag type")
	}
	return v.GetSlice(), nil
}

// Flag is one install flag as the choices hold it: its name, without the dashes, and
// its values, in the order they were given. Repeated is a flag that may be given more
// than once.
type Flag struct {
	Name     string
	Values   []string
	Repeated bool
	// Given is whether the command line, or a screen, set it explicitly.
	Given bool
}

// Flags is every install choice flag, in the same stable order the equivalent command
// uses. A screen reads it to show a flag it has no editor for, so nothing is dropped.
// Its values include credentials: a caller draws a value only when SecretValue says it
// is not one.
func (c Choices) Flags() []Flag {
	fv := c.flagValues()
	out := make([]Flag, len(fv))
	for i, f := range fv {
		out[i] = Flag{Name: f.name, Values: append([]string(nil), f.values...), Repeated: f.slice, Given: c.Given.Has(f.name)}
	}
	return out
}

// SecretValue reports whether a flag's value must never be shown: a webhook URL, or any
// URL carrying userinfo, a query or a fragment. The equivalent command replaces exactly
// these with a placeholder.
func SecretValue(name, value string) bool { return secretFlagValue(name, value) }
