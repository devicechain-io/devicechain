// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package product

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"
)

// ManifestFile is the bundle's manifest, at the bundle root.
const ManifestFile = "product.yaml"

// The per-kind directories. A bundle holds one file per resource, named <token>.<ext>.
const (
	dirDeviceTypes  = "device-types"
	dirProfiles     = "profiles"
	dirIntegrations = "integrations"
	dirConnectors   = "connectors"
	dirDashboards   = "dashboards"
)

var managedDirs = []string{dirDeviceTypes, dirProfiles, dirIntegrations, dirConnectors, dirDashboards}

// tokenRe is what a token must look like to be a file name: it is also a stricter subset of
// every API's token grammar, so a token that passes here is one the API accepts.
var tokenRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

// Normalize puts every list in its canonical order (by token, or key) and every document in
// canonical form, so a bundle read from disk and one built from a tenant compare equal.
func (b *Bundle) Normalize() error {
	if b.Product.APIVersion == "" {
		b.Product.APIVersion = APIVersion
	}
	if b.Product.Kind == "" {
		b.Product.Kind = "Product"
	}
	var errs []error
	canon := func(where string, d *Doc) {
		c, err := Canon(*d)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", where, err))
			return
		}
		*d = c
	}
	for i := range b.DeviceTypes {
		t := &b.DeviceTypes[i]
		t.Kind = string(KindDeviceType)
		canon("device type "+t.Token+" metadata", &t.Metadata)
	}
	for i := range b.Profiles {
		p := &b.Profiles[i]
		p.Kind = string(KindProfile)
		w := "profile " + p.Token
		canon(w+" metadata", &p.Metadata)
		canon(w+" decoder", &p.Decoder)
		for j := range p.Metrics {
			canon(w+" metric "+p.Metrics[j].Token+" enum", &p.Metrics[j].Enum)
			p.Metrics[j].Enum = noEmptyEnum(p.Metrics[j].Enum)
			canon(w+" metric "+p.Metrics[j].Token+" metadata", &p.Metrics[j].Metadata)
		}
		for j := range p.CalculatedMetrics {
			canon(w+" calculated metric "+p.CalculatedMetrics[j].Token+" enum", &p.CalculatedMetrics[j].Enum)
			p.CalculatedMetrics[j].Enum = noEmptyEnum(p.CalculatedMetrics[j].Enum)
			canon(w+" calculated metric "+p.CalculatedMetrics[j].Token+" metadata", &p.CalculatedMetrics[j].Metadata)
		}
		for j := range p.Commands {
			if p.Commands[j].Impact == "" {
				p.Commands[j].Impact = "NORMAL" // the server's default, so an omitted impact compares equal
			}
			canon(w+" command "+p.Commands[j].Token+" parameterSchema", &p.Commands[j].ParameterSchema)
			canon(w+" command "+p.Commands[j].Token+" metadata", &p.Commands[j].Metadata)
		}
		for j := range p.Rules {
			r := &p.Rules[j]
			canon(w+" rule "+r.Token+" definition", &r.Definition)
			canon(w+" rule "+r.Token+" authoringGraph", &r.AuthoringGraph)
			canon(w+" rule "+r.Token+" metadata", &r.Metadata)
		}
		sort.Slice(p.ConfigurationDeclaration, func(i, j int) bool {
			return p.ConfigurationDeclaration[i].Key < p.ConfigurationDeclaration[j].Key
		})
		sort.Slice(p.Metrics, func(i, j int) bool { return p.Metrics[i].Token < p.Metrics[j].Token })
		sort.Slice(p.CalculatedMetrics, func(i, j int) bool { return p.CalculatedMetrics[i].Token < p.CalculatedMetrics[j].Token })
		sort.Slice(p.Commands, func(i, j int) bool { return p.Commands[i].Token < p.Commands[j].Token })
		sort.Slice(p.Rules, func(i, j int) bool { return p.Rules[i].Token < p.Rules[j].Token })
	}
	for i := range b.Integrations {
		in := &b.Integrations[i]
		in.Kind = string(KindIntegration)
		if in.Identity == "" && (in.Type == "CHIRPSTACK_V4" || in.Type == "TTS_V3") {
			in.Identity = "DEV_EUI" // the server's default for the network-server types
		}
		canon("integration "+in.Token+" generic", &in.Generic)
		canon("integration "+in.Token+" metadata", &in.Metadata)
		sort.Strings(in.DeviceTypes)
	}
	for i := range b.Connectors {
		c := &b.Connectors[i]
		c.Kind = string(KindConnector)
		canon("connector "+c.Token+" config", &c.Config)
	}
	for i := range b.Dashboards {
		d := &b.Dashboards[i]
		d.Kind = string(KindDashboard)
		canon("dashboard "+d.Token+" definition", &d.Definition)
	}
	sort.Slice(b.DeviceTypes, func(i, j int) bool { return b.DeviceTypes[i].Token < b.DeviceTypes[j].Token })
	sort.Slice(b.Profiles, func(i, j int) bool { return b.Profiles[i].Token < b.Profiles[j].Token })
	sort.Slice(b.Integrations, func(i, j int) bool { return b.Integrations[i].Token < b.Integrations[j].Token })
	sort.Slice(b.Connectors, func(i, j int) bool { return b.Connectors[i].Token < b.Connectors[j].Token })
	sort.Slice(b.Dashboards, func(i, j int) bool { return b.Dashboards[i].Token < b.Dashboards[j].Token })
	return errors.Join(errs...)
}

// Validate checks what can be checked without a tenant: shapes, required fields and the
// uniqueness the server enforces, so a mistake is caught before anything is sent. The server
// remains the authority on everything else.
func (b *Bundle) Validate() error {
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	if b.Product.APIVersion != APIVersion {
		bad("%s: apiVersion %q is not supported by this build (it reads %s)", ManifestFile, b.Product.APIVersion, APIVersion)
	}
	if b.Product.Kind != "Product" {
		bad("%s: kind must be Product, got %q", ManifestFile, b.Product.Kind)
	}
	seen := map[string]string{}
	claim := func(kind Kind, tok string) {
		if !tokenRe.MatchString(tok) {
			bad("%s %q: a token is letters, digits, hyphens and underscores, starting with a letter or digit, at most 128 characters", kind, tok)
			return
		}
		// A token is unique per family tenant-wide, and a file name must not collide on a
		// case-insensitive file system either.
		k := string(kind) + "/" + strings.ToLower(tok)
		if prev, dup := seen[k]; dup {
			bad("%s token %q collides with %q (tokens are compared case-insensitively here because they are file names)", kind, tok, prev)
		}
		seen[k] = tok
	}
	for _, t := range b.DeviceTypes {
		claim(KindDeviceType, t.Token)
		if t.Metadata != nil && !isObject(t.Metadata) {
			bad("device type %q: metadata must be a JSON object", t.Token)
		}
	}
	// The child families share one token namespace each across ALL profiles.
	for _, p := range b.Profiles {
		claim(KindProfile, p.Token)
		keys := map[string]string{}
		ckeys := map[string]string{}
		for _, m := range p.Metrics {
			claim(KindMetric, m.Token)
			if m.Key == "" || m.DataType == "" {
				bad("profile %q metric %q: key and dataType are required", p.Token, m.Token)
			}
			if prev, dup := keys[m.Key]; dup {
				bad("profile %q: metric key %q is declared by both %q and %q", p.Token, m.Key, prev, m.Token)
			}
			keys[m.Key] = m.Token
			if m.Enum != nil && !isStringArray(m.Enum) {
				bad("profile %q metric %q: enum must be a JSON array of strings", p.Token, m.Token)
			}
		}
		for _, m := range p.CalculatedMetrics {
			claim(KindMetric, m.Token)
			if m.Key == "" || m.DataType == "" || strings.TrimSpace(m.Expression) == "" {
				bad("profile %q calculated metric %q: key, dataType and expression are required", p.Token, m.Token)
			}
			if prev, dup := keys[m.Key]; dup {
				bad("profile %q: metric key %q is declared by both %q and %q", p.Token, m.Key, prev, m.Token)
			}
			keys[m.Key] = m.Token
			if m.Enum != nil && !isStringArray(m.Enum) {
				bad("profile %q calculated metric %q: enum must be a JSON array of strings", p.Token, m.Token)
			}
		}
		for _, c := range p.Commands {
			claim(KindCommand, c.Token)
			if c.Key == "" {
				bad("profile %q command %q: key is required", p.Token, c.Token)
			}
			if prev, dup := ckeys[c.Key]; dup {
				bad("profile %q: command key %q is declared by both %q and %q", p.Token, c.Key, prev, c.Token)
			}
			ckeys[c.Key] = c.Token
		}
		for _, r := range p.Rules {
			claim(KindRule, r.Token)
			if len(r.Definition) == 0 {
				bad("profile %q rule %q: definition is required", p.Token, r.Token)
			}
			if (r.EntityGroupToken == "") != (r.EntityGroupVersion == nil) {
				bad("profile %q rule %q: entityGroupToken and entityGroupVersion are set together or not at all", p.Token, r.Token)
			}
		}
		cfg := map[string]bool{}
		for _, k := range p.ConfigurationDeclaration {
			if k.Key == "" || k.ValueType == "" {
				bad("profile %q configurationDeclaration: key and valueType are required", p.Token)
			}
			if cfg[k.Key] {
				bad("profile %q configurationDeclaration: key %q is declared twice", p.Token, k.Key)
			}
			cfg[k.Key] = true
		}
		if p.Decoder != nil && !isObject(p.Decoder) {
			bad("profile %q: decoder must be a JSON object", p.Token)
		}
	}
	for _, in := range b.Integrations {
		claim(KindIntegration, in.Token)
		if in.Type == "" {
			bad("integration %q: type is required", in.Token)
		}
		if len(in.DeviceTypes) == 0 {
			bad("integration %q: at least one device type is required", in.Token)
		}
	}
	for _, c := range b.Connectors {
		claim(KindConnector, c.Token)
		if c.Type == "" || !isObject(c.Config) {
			bad("connector %q: type and a JSON-object config are required", c.Token)
		}
		if c.Credential != nil {
			if _, err := parseHandle(c.Credential.Handle); err != nil {
				bad("connector %q: %v", c.Token, err)
			}
		}
	}
	for _, d := range b.Dashboards {
		claim(KindDashboard, d.Token)
		if len(d.Definition) == 0 {
			bad("dashboard %q: definition is required", d.Token)
		}
	}
	return errors.Join(errs...)
}

// noEmptyEnum treats an empty allowed-values list as none: the server reads it as "any value
// is allowed" and a published version reports it as absent, so the two must compare equal.
func noEmptyEnum(d Doc) Doc {
	if string(d) == "[]" {
		return nil
	}
	return d
}

func isObject(d Doc) bool { return len(d) > 0 && d[0] == '{' }

func isStringArray(d Doc) bool {
	var v []string
	return json.Unmarshal(d, &v) == nil && v != nil
}

// parseHandle resolves a credential handle's scheme. Only env: exists in this build; any
// other scheme is refused rather than treated as a literal, since a handle that silently
// became a value would be exactly the leak this design prevents.
func parseHandle(h string) (envVar string, err error) {
	// The messages never repeat the handle: a handle is exactly where someone who has pasted a
	// secret value would have put it, and an error is printed.
	scheme, rest, ok := strings.Cut(h, ":")
	if !ok || rest == "" {
		return "", fmt.Errorf("credential handle is not of the form scheme:name")
	}
	if scheme != "env" {
		return "", fmt.Errorf("credential handle uses a scheme this build does not support (only env:NAME)")
	}
	if !envVarRe.MatchString(rest) {
		return "", fmt.Errorf("credential handle env:NAME does not name an environment variable")
	}
	return rest, nil
}

var envVarRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Write writes the bundle to dir. An empty or absent dir is fine; a directory that already
// holds a bundle (a product.yaml) is rewritten in place, with the files of resources that no
// longer exist removed; any other non-empty directory is refused, so an export cannot be
// pointed at someone's source tree.
func (b *Bundle) Write(dir string) error {
	if err := b.Normalize(); err != nil {
		return err
	}
	if err := b.Validate(); err != nil {
		return err
	}
	if err := scanBundle(b); err != nil {
		return err
	}
	ents, err := os.ReadDir(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	case err != nil:
		return err
	case len(ents) > 0:
		if _, err := os.Stat(filepath.Join(dir, ManifestFile)); err != nil {
			return fmt.Errorf("%s is not empty and holds no %s: refusing to write a bundle into it", dir, ManifestFile)
		}
		if fi, err := os.Lstat(filepath.Join(dir, ManifestFile)); err != nil || !fi.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file: refusing to write a bundle through it", filepath.Join(dir, ManifestFile))
		}
		// Check every managed directory BEFORE removing anything from any of them, so a
		// refusal cannot leave an existing bundle half-emptied.
		for _, d := range managedDirs {
			if err := checkFilesOnly(dir, filepath.Join(dir, d)); err != nil {
				return err
			}
		}
		for _, d := range managedDirs {
			if err := removeFiles(dir, filepath.Join(dir, d)); err != nil {
				return err
			}
		}
	}
	files := map[string][]byte{}
	put := func(rel string, data []byte) { files[filepath.ToSlash(rel)] = data }
	mb, err := yaml.Marshal(b.Product)
	if err != nil {
		return err
	}
	put(ManifestFile, mb)
	for _, t := range b.DeviceTypes {
		d, err := yaml.Marshal(t)
		if err != nil {
			return err
		}
		put(dirDeviceTypes+"/"+t.Token+".yaml", d)
	}
	for _, p := range b.Profiles {
		d, err := yaml.Marshal(p)
		if err != nil {
			return err
		}
		put(dirProfiles+"/"+p.Token+".yaml", d)
	}
	for _, in := range b.Integrations {
		d, err := yaml.Marshal(in)
		if err != nil {
			return err
		}
		put(dirIntegrations+"/"+in.Token+".yaml", d)
	}
	for _, c := range b.Connectors {
		d, err := yaml.Marshal(c)
		if err != nil {
			return err
		}
		put(dirConnectors+"/"+c.Token+".yaml", d)
	}
	for _, d := range b.Dashboards {
		j, err := json.MarshalIndent(d, "", "  ")
		if err != nil {
			return err
		}
		put(dirDashboards+"/"+d.Token+".json", append(j, '\n'))
	}
	for rel, data := range files {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// confined resolves d and root through any symlinks and reports an error unless d lies
// inside root. A managed directory that is, or sits behind, a link to somewhere else would
// otherwise let an export delete files that are not part of the bundle.
func confined(root, d string) error {
	rr, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	rd, err := filepath.EvalSymlinks(d)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(rr, rd)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("%s resolves outside the bundle (%s): refusing to touch it", d, rd)
	}
	return nil
}

// checkFilesOnly reports whether d, a managed kind directory of the bundle at root, is a
// real directory inside it holding nothing but regular files (or does not exist). A symlink,
// whether the directory itself or an entry in it, is refused rather than followed.
func checkFilesOnly(root, d string) error {
	fi, err := os.Lstat(d)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return fmt.Errorf("%s is a symbolic link or not a directory: refusing to export through it", d)
	}
	if err := confined(root, d); err != nil {
		return err
	}
	ents, err := os.ReadDir(d)
	if err != nil {
		return err
	}
	for _, e := range ents {
		if !e.Type().IsRegular() {
			return fmt.Errorf("%s: %q is not a regular file (a directory or symbolic link) and cannot be part of a bundle", d, e.Name())
		}
	}
	return nil
}

// removeFiles deletes the regular files directly inside d (a managed kind directory of the
// bundle at root). It re-checks each entry as it goes, so a link planted after the check is
// still never followed.
func removeFiles(root, d string) error {
	if err := checkFilesOnly(root, d); err != nil {
		return err
	}
	ents, err := os.ReadDir(d)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	// Remove through an os.Root: even a link swapped in after the checks cannot take a delete
	// outside the bundle.
	rt, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer rt.Close()
	for _, e := range ents {
		p := filepath.Join(d, e.Name())
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file: refusing to delete it", p)
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if err := rt.Remove(rel); err != nil {
			return err
		}
	}
	return nil
}

// Read loads and validates the bundle in dir. It is strict: an unknown field, an unknown
// directory, a file whose name is not its token, or a file in the wrong directory is an error.
func Read(dir string) (*Bundle, error) {
	mb, err := os.ReadFile(filepath.Join(dir, ManifestFile))
	if err != nil {
		return nil, fmt.Errorf("%s is not a product bundle: %w", dir, err)
	}
	b := &Bundle{}
	if err := yaml.UnmarshalStrict(mb, &b.Product); err != nil {
		return nil, fmt.Errorf("%s: %w", ManifestFile, err)
	}
	// Refuse a bundle this build does not understand BEFORE parsing its files with a shape
	// that may differ.
	if b.Product.APIVersion != APIVersion {
		return nil, fmt.Errorf("%s: apiVersion %q is not supported by this build (it reads %s)", ManifestFile, b.Product.APIVersion, APIVersion)
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	known := map[string]bool{}
	for _, d := range managedDirs {
		known[d] = true
	}
	for _, e := range ents {
		if e.IsDir() && !known[e.Name()] && !strings.HasPrefix(e.Name(), ".") {
			return nil, fmt.Errorf("unknown directory %q in the bundle (expected %s)", e.Name(), strings.Join(managedDirs, ", "))
		}
	}
	var errs []error
	load := func(sub, ext string, into func(name string, data []byte) error) {
		d := filepath.Join(dir, sub)
		fs, err := os.ReadDir(d)
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		if err != nil {
			errs = append(errs, err)
			return
		}
		for _, f := range fs {
			if f.Name() == ".gitkeep" {
				continue
			}
			if f.IsDir() || filepath.Ext(f.Name()) != ext {
				errs = append(errs, fmt.Errorf("%s/%s: only %s files are allowed here", sub, f.Name(), ext))
				continue
			}
			data, err := os.ReadFile(filepath.Join(d, f.Name()))
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if err := into(strings.TrimSuffix(f.Name(), ext), data); err != nil {
				errs = append(errs, fmt.Errorf("%s/%s: %w", sub, f.Name(), err))
			}
		}
	}
	check := func(name, token, kind string, want Kind) error {
		if kind != string(want) {
			return fmt.Errorf("kind is %q, expected %s", kind, want)
		}
		if name != token {
			return fmt.Errorf("the file name must be the token: file %q holds token %q", name, token)
		}
		return nil
	}
	load(dirDeviceTypes, ".yaml", func(n string, data []byte) error {
		var v DeviceType
		if err := yaml.UnmarshalStrict(data, &v); err != nil {
			return err
		}
		b.DeviceTypes = append(b.DeviceTypes, v)
		return check(n, v.Token, v.Kind, KindDeviceType)
	})
	load(dirProfiles, ".yaml", func(n string, data []byte) error {
		var v Profile
		if err := yaml.UnmarshalStrict(data, &v); err != nil {
			return err
		}
		b.Profiles = append(b.Profiles, v)
		return check(n, v.Token, v.Kind, KindProfile)
	})
	load(dirIntegrations, ".yaml", func(n string, data []byte) error {
		var v Integration
		if err := yaml.UnmarshalStrict(data, &v); err != nil {
			return err
		}
		b.Integrations = append(b.Integrations, v)
		return check(n, v.Token, v.Kind, KindIntegration)
	})
	load(dirConnectors, ".yaml", func(n string, data []byte) error {
		var v Connector
		if err := yaml.UnmarshalStrict(data, &v); err != nil {
			return err
		}
		b.Connectors = append(b.Connectors, v)
		return check(n, v.Token, v.Kind, KindConnector)
	})
	load(dirDashboards, ".json", func(n string, data []byte) error {
		var v Dashboard
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&v); err != nil {
			return err
		}
		if dec.More() {
			return fmt.Errorf("unexpected content after the dashboard document")
		}
		b.Dashboards = append(b.Dashboards, v)
		return check(n, v.Token, v.Kind, KindDashboard)
	})
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	if err := b.Normalize(); err != nil {
		return nil, err
	}
	if err := b.Validate(); err != nil {
		return nil, err
	}
	return b, nil
}
