package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompilerProbeCannotDirtyCheckout(t *testing.T) {
	dir := t.TempDir()
	adapter := filepath.Join(dir, "release-cc")
	if raw, err := exec.Command("go", "build", "-o", adapter, ".").CombinedOutput(); err != nil {
		t.Fatalf("build adapter: %v %s", err, raw)
	}
	compiler := filepath.Join(dir, "compiler")
	// Simulate a compiler that creates an implicit object as well as its explicit
	// output. Relative source/output paths must still refer to the caller.
	if err := os.WriteFile(compiler, []byte("#!/bin/sh\npwd > \"$PRESSURE_CC_TRACE\"\ntouch -- -.o\ncat \"$1\" > \"$3\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "source.c")
	if err := os.WriteFile(source, []byte("input preserved"), 0600); err != nil {
		t.Fatal(err)
	}
	trace := filepath.Join(dir, "trace")
	c := exec.Command(adapter, "source.c", "-o", "result.o")
	c.Dir = dir
	c.Env = append(os.Environ(), "PRESSURE_TARGET_CC="+compiler, "PRESSURE_CC_TRACE="+trace)
	if raw, err := c.CombinedOutput(); err != nil {
		t.Fatalf("adapter: %v %s", err, raw)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "result.o"))
	if err != nil || string(raw) != "input preserved" {
		t.Fatalf("relative paths lost: %q %v", raw, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "-.o")); !os.IsNotExist(err) {
		t.Fatalf("compiler dirtied checkout: %v", err)
	}
	scratch, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(strings.TrimSpace(string(scratch))); !os.IsNotExist(err) {
		t.Fatalf("compiler scratch retained: %v", err)
	}
}
