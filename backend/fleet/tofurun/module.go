// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package tofurun

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

const (
	maxModuleFiles = 2000
	maxModuleBytes = 20 << 20
	lockFileName   = ".terraform.lock.hcl"
)

// refusedName reports why a file may not be in a baked root. A root is code: state, plans and
// variable files are inputs that belong to a run, and one found in the tree means the tree was
// built from a working directory (the way `go:embed` of a directory ships whatever is on disk).
func refusedName(rel string) string {
	base := strings.ToLower(filepath.Base(rel))
	switch {
	case strings.Contains(rel, ".terraform/") || base == ".terraform":
		return "a .terraform directory"
	case strings.HasSuffix(base, ".tfstate") || strings.Contains(base, ".tfstate."):
		return "a state file"
	case strings.HasSuffix(base, ".tfplan") || base == "tfplan":
		return "a saved plan"
	case strings.HasSuffix(base, ".tfvars") || strings.HasSuffix(base, ".tfvars.json"):
		return "a variable file (inputs arrive as environment, never from the tree)"
	case base == ".terraformrc" || base == "terraform.rc":
		return "a CLI config file"
	case strings.HasSuffix(base, ".override.tf") || base == "override.tf" || strings.HasSuffix(base, "_override.tf") ||
		strings.HasSuffix(base, ".override.tofu") || base == "override.tofu" || strings.HasSuffix(base, "_override.tofu"):
		return "an override file"
	}
	return ""
}

type moduleFile struct {
	rel  string
	size int64
}

func walkModule(root string) ([]moduleFile, error) {
	var files []moduleFile
	var total int64
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s is a symbolic link", ErrModuleRefused, rel)
		}
		probe := rel
		if d.IsDir() {
			probe += "/"
		}
		if why := refusedName(probe); why != "" {
			return fmt.Errorf("%w: %s holds %s", ErrModuleRefused, rel, why)
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%w: %s is not a regular file", ErrModuleRefused, rel)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		if len(files) >= maxModuleFiles || total > maxModuleBytes {
			return fmt.Errorf("%w: the tree is larger than %d files or %d bytes", ErrModuleRefused, maxModuleFiles, maxModuleBytes)
		}
		files = append(files, moduleFile{rel: rel, size: info.Size()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].rel < files[j].rel })
	// OpenTofu reads foo.tofu INSTEAD of foo.tf when both exist: a .tofu file beside a .tf of the
	// same name would silently replace the baked one.
	have := make(map[string]bool, len(files))
	for _, f := range files {
		have[f.rel] = true
	}
	for _, f := range files {
		if strings.HasSuffix(f.rel, ".tofu") && have[strings.TrimSuffix(f.rel, ".tofu")+".tf"] {
			return nil, fmt.Errorf("%w: %s shadows %s.tf", ErrModuleRefused, f.rel, strings.TrimSuffix(f.rel, ".tofu"))
		}
	}
	return files, nil
}

// cleanRootPath validates the path of the root inside the tree: relative, slash-separated, never
// leaving the tree. "" and "." both mean the tree itself.
func cleanRootPath(p string) (string, error) {
	if p == "" {
		return ".", nil
	}
	c := path.Clean(p)
	if path.IsAbs(c) || c == ".." || strings.HasPrefix(c, "../") || strings.Contains(p, "\\") {
		return "", fmt.Errorf("%w: the root path %q leaves the tree", ErrModuleRefused, p)
	}
	return c, nil
}

// inspectModule is inspectTree for a tree that is itself the root.
func inspectModule(root string) (digest, lockHash string, err error) { return inspectTree(root, ".") }

// inspectTree validates the tree and returns its digest (over every file's path and bytes, in path
// order) and the hash of the provider lock file of the root at rootPath ("" when it has none). A
// root's relative module sources ("../../opentofu/modules/x") resolve inside the tree, which is why
// the whole tree is copied, digested and run from rootPath.
func inspectTree(root, rootPath string) (digest, lockHash string, err error) {
	rootPath, err = cleanRootPath(rootPath)
	if err != nil {
		return "", "", err
	}
	lockRel := path.Join(rootPath, lockFileName)
	if root == "" {
		return "", "", fmt.Errorf("%w: no root directory given", ErrModuleRefused)
	}
	if fi, serr := os.Stat(root); serr != nil || !fi.IsDir() {
		return "", "", fmt.Errorf("%w: %q is not a directory", ErrModuleRefused, root)
	}
	files, err := walkModule(root)
	if err != nil {
		return "", "", err
	}
	if len(files) == 0 {
		return "", "", fmt.Errorf("%w: the tree is empty", ErrModuleRefused)
	}
	if fi, serr := os.Stat(filepath.Join(root, filepath.FromSlash(rootPath))); serr != nil || !fi.IsDir() {
		return "", "", fmt.Errorf("%w: the root path %q is not a directory in the tree", ErrModuleRefused, rootPath)
	}
	h := sha256.New()
	for _, f := range files {
		fh, oerr := os.Open(filepath.Join(root, filepath.FromSlash(f.rel)))
		if oerr != nil {
			return "", "", oerr
		}
		fileHash := sha256.New()
		_, cerr := io.Copy(fileHash, fh)
		fh.Close()
		if cerr != nil {
			return "", "", cerr
		}
		sum := hex.EncodeToString(fileHash.Sum(nil))
		fmt.Fprintf(h, "%s\x00%d\x00%s\n", f.rel, f.size, sum)
		if f.rel == lockRel {
			lockHash = sum
		}
	}
	return hex.EncodeToString(h.Sum(nil)), lockHash, nil
}

// copyModule copies the tree into dst (an existing directory) and requires the copy to have the
// digest the run was admitted with, so a tree that changed after admission is not run.
func copyModule(src, dst, want string) error {
	files, err := walkModule(src)
	if err != nil {
		return err
	}
	for _, f := range files {
		to := filepath.Join(dst, filepath.FromSlash(f.rel))
		if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
			return err
		}
		b, err := os.ReadFile(filepath.Join(src, filepath.FromSlash(f.rel)))
		if err != nil {
			return err
		}
		if err := os.WriteFile(to, b, 0o600); err != nil {
			return err
		}
	}
	after, _, err := inspectModule(dst)
	if err != nil {
		return err
	}
	if after != want {
		return fmt.Errorf("%w: the tree is not the one the run was admitted with", ErrModuleRefused)
	}
	return nil
}

// CheckRoot reports whether rootPath in tree is an acceptable baked root: nothing a root must not hold, and a
// provider lock file (a root with none would resolve its providers at run time).
func CheckRoot(tree, rootPath string) error {
	_, lock, err := inspectTree(tree, rootPath)
	if err != nil {
		return err
	}
	if lock == "" {
		return fmt.Errorf("%w: no %s", ErrModuleRefused, lockFileName)
	}
	return nil
}
