// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"compress/gzip"
	"crypto/sha256"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/devicechain-io/dc-simulator/sim"
)

func defaultOutput() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.work")); err == nil {
			return filepath.Join(dir, "backend/services/event-processing/internal/review/testdata"), nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("cannot find repository root (go.work); pass -out")
		}
		dir = parent
	}
}
func run() error {
	out := flag.String("out", "", "capture output directory (default: repository review/testdata)")
	check := flag.Bool("check", false, "regenerate in memory and fail on hash drift")
	flag.Parse()
	if *out == "" {
		dir, err := defaultOutput()
		if err != nil {
			return err
		}
		*out = dir
	}
	raw, err := sim.GenerateNightShiftCapture()
	if err != nil {
		return err
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(raw))
	path := filepath.Join(*out, "night-shift.capture.sha256")
	if *check {
		old, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(old)) != hash {
			return fmt.Errorf("capture hash drift: generated %s, recorded %s", hash, strings.TrimSpace(string(old)))
		}
		fmt.Println(hash)
		return nil
	}
	if err := os.MkdirAll(*out, 0755); err != nil {
		return err
	}
	f, err := os.Create(filepath.Join(*out, "night-shift.capture.jsonl.gz"))
	if err != nil {
		return err
	}
	z := gzip.NewWriter(f) // zero ModTime and empty Name/Comment: deterministic header.
	if _, err := z.Write(raw); err != nil {
		z.Close()
		f.Close()
		return err
	}
	if err := z.Close(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(hash+"\n"), 0644); err != nil {
		return err
	}
	fmt.Println(hash)
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
