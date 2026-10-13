//go:build linux

package main

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLinuxTrashInfoAndVolume(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "xdg"))
	source := filepath.Join(root, ".git-shed-stage-"+newID())
	write(t, filepath.Join(source, "file"), "sample\n")
	dest, info, e := systemTrash(source)
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(dest)
	defer os.Remove(info)
	b, e := os.ReadFile(info)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(b), "[Trash Info]\nPath="+(&url.URL{Path: source}).EscapedPath()+"\nDeletionDate=") {
		t.Fatal(string(b))
	}
	if filepath.Base(info) != filepath.Base(dest)+".trashinfo" {
		t.Fatal("metadata mismatch")
	}
	volume := os.Getenv("GIT_SHED_VOLUME_TEST")
	if volume == "" {
		return
	}
	a, e := device(volume)
	if e != nil {
		t.Fatal(e)
	}
	bdev, e := device(root)
	if e != nil {
		t.Fatal(e)
	}
	if a == bdev {
		t.Fatal("volume test needs separate filesystem")
	}
	source = filepath.Join(volume, ".git-shed-stage-"+newID())
	write(t, filepath.Join(source, "file"), "volume\n")
	dest, info, e = systemTrash(source)
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(dest)
	defer os.Remove(info)
	if !inside(volume, dest) {
		t.Fatal("source copied to another filesystem")
	}
	b, e = os.ReadFile(info)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(b), "Path="+filepath.Base(source)+"\n") {
		t.Fatal(string(b))
	}
}
