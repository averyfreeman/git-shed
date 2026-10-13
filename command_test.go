package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestScanRootDefaultsToCurrentDirectoryAndSupportsOverrides(t *testing.T) {
	base := t.TempDir()
	current := filepath.Join(base, "current")
	flagRoot := filepath.Join(base, "flag-root")
	positionalRoot := filepath.Join(base, "positional-root")
	for _, path := range []string{current, flagRoot, positionalRoot} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	canonicalCurrent, err := canonical(current)
	if err != nil {
		t.Fatal(err)
	}
	canonicalFlagRoot, err := canonical(flagRoot)
	if err != nil {
		t.Fatal(err)
	}
	canonicalPositionalRoot, err := canonical(positionalRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(canonicalCurrent)

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "current directory by default", args: []string{"scan", "--format", "json"}, want: canonicalCurrent},
		{name: "root flag override", args: []string{"scan", "--root", flagRoot, "--format", "json"}, want: canonicalFlagRoot},
		{name: "positional root override", args: []string{"scan", "--format", "json", positionalRoot}, want: canonicalPositionalRoot},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if err := run(context.Background(), tt.args, &stdout, &stderr, strings.NewReader("")); err != nil {
				t.Fatal(err)
			}
			var report Report
			if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
				t.Fatalf("decode scan report: %v\n%s", err, stdout.String())
			}
			if report.Root != tt.want {
				t.Fatalf("scan root = %q, want %q", report.Root, tt.want)
			}
		})
	}
}

func TestScanHelpShowsPortableCurrentDirectoryDefault(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), []string{"scan", "--help"}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), `default "."`) {
		t.Fatalf("scan help does not show current-directory default:\n%s", stderr.String())
	}
}

func TestCommandTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, e := command(ctx, "", "sh", "-c", "sleep 10")
	if e == nil || !strings.Contains(e.Error(), "deadline exceeded") {
		t.Fatal(e)
	}
	if time.Since(start) > time.Second {
		t.Fatal("timeout left subprocess pipes open")
	}
}
