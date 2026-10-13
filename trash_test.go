package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func testStore(t *testing.T, f fixture) Store {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", filepath.Join(f.root, "xdg"))
	return Store{filepath.Join(f.root, "journal")}
}
func cleanupEntry(t *testing.T, s Store, e *Entry) {
	t.Helper()
	t.Cleanup(func() {
		if e.Trash != "" {
			_ = os.RemoveAll(e.Trash)
		}
		if e.Info != "" {
			_ = os.Remove(e.Info)
		}
		if e.Stage != "" {
			_ = os.RemoveAll(e.Stage)
		}
	})
}
func TestTrashLeaveRestoreAndPurge(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("native Trash")
	}
	f := repoFixture(t)
	s := testStore(t, f)
	write(t, filepath.Join(f.local, ".env"), "keep-secret\n")
	write(t, filepath.Join(f.local, "untracked.txt"), "preserve\n")
	r := inspectFixture(t, f)
	entry, e := TrashRepo(context.Background(), s, r, "leave", "", false)
	cleanupEntry(t, s, &entry)
	if e != nil {
		t.Fatal(e)
	}
	if !exists(filepath.Join(f.local, ".env")) || !exists(filepath.Join(f.local, "untracked.txt")) || exists(filepath.Join(f.local, ".git")) {
		t.Fatal("leave mode did not preserve local files")
	}
	if e = Restore(context.Background(), s, &entry, ""); e != nil {
		t.Fatal(e)
	}
	if !exists(filepath.Join(f.local, ".git")) || entry.State != "restored" {
		t.Fatal("restore failed")
	}
	r = inspectFixture(t, f)
	second, e := TrashRepo(context.Background(), s, r, "leave", "", false)
	cleanupEntry(t, s, &second)
	if e != nil {
		t.Fatal(e)
	}
	unrelated := filepath.Join(filepath.Dir(second.Trash), "git-shed-unrelated-"+second.ID)
	write(t, unrelated, "keep")
	defer os.Remove(unrelated)
	if e = Purge(context.Background(), s, &second); e != nil {
		t.Fatal(e)
	}
	if exists(second.Trash) || !exists(unrelated) || !exists(filepath.Join(f.local, ".env")) {
		t.Fatal("purge affected unrelated or preserved files")
	}
}
func TestTrashAllRequiresReviewAndDigestProtectsPurge(t *testing.T) {
	f := repoFixture(t)
	s := testStore(t, f)
	write(t, filepath.Join(f.local, ".env"), "secret\n")
	r := inspectFixture(t, f)
	_, e := TrashRepo(context.Background(), s, r, "all", "", false)
	if e == nil {
		t.Fatal("all requires review")
	}
	entry, e := TrashRepo(context.Background(), s, r, "all", "", true)
	cleanupEntry(t, s, &entry)
	if e != nil {
		t.Fatal(e)
	}
	if exists(f.local) {
		t.Fatal("whole checkout not moved")
	}
	write(t, filepath.Join(entry.Trash, "unexpected"), "new local data")
	if e = Purge(context.Background(), s, &entry); e == nil || !strings.Contains(e.Error(), "changed") {
		t.Fatalf("modified payload must block purge: %v", e)
	}
	if e = Restore(context.Background(), s, &entry, ""); e != nil {
		t.Fatal(e)
	}
	if !exists(filepath.Join(f.local, ".env")) {
		t.Fatal("all restore missing local data")
	}
}
func TestBackupAndRestoreCollision(t *testing.T) {
	f := repoFixture(t)
	s := testStore(t, f)
	write(t, filepath.Join(f.local, "cache", "artifact"), "preserve\n")
	write(t, filepath.Join(f.local, "untracked.txt"), "local\n")
	r := inspectFixture(t, f)
	entry, e := TrashRepo(context.Background(), s, r, "backup", filepath.Join(f.root, "backups"), false)
	cleanupEntry(t, s, &entry)
	if e != nil {
		t.Fatal(e)
	}
	if !exists(filepath.Join(entry.Backup, "cache", "artifact")) || exists(f.local) {
		t.Fatal("backup files not moved")
	}
	write(t, filepath.Join(f.local, "file.txt"), "collision\n")
	if e = Restore(context.Background(), s, &entry, ""); e == nil {
		t.Fatal("restore must refuse overwrite")
	}
	if !exists(filepath.Join(entry.Trash, "checkout", ".git")) {
		t.Fatal("restore changed files before collision preflight")
	}
	_ = os.Remove(filepath.Join(f.local, "file.txt"))
	if e = Restore(context.Background(), s, &entry, ""); e != nil {
		t.Fatal(e)
	}
	if !exists(filepath.Join(f.local, "cache", "artifact")) {
		t.Fatal("backup restore failed")
	}
}
func TestTrashStaleStateAndDirtyBlocked(t *testing.T) {
	f := repoFixture(t)
	s := testStore(t, f)
	r := inspectFixture(t, f)
	write(t, filepath.Join(f.local, "file.txt"), "changed\n")
	_, e := TrashRepo(context.Background(), s, r, "leave", "", false)
	if e == nil || !strings.Contains(e.Error(), "changed") {
		t.Fatalf("stale preview must fail: %v", e)
	}
	dirty := inspectFixture(t, f)
	_, e = TrashRepo(context.Background(), s, dirty, "leave", "", false)
	if e == nil {
		t.Fatal("dirty must fail")
	}
	if !exists(filepath.Join(f.local, ".git")) {
		t.Fatal("blocked trash changed repository")
	}
}
func TestPurgeDeletedRemoteAndRecoveryCommits(t *testing.T) {
	f := repoFixture(t)
	s := testStore(t, f)
	write(t, filepath.Join(f.local, "file.txt"), "recovery\n")
	mustGit(t, f.local, "commit", "-am", "recovery")
	mustGit(t, f.local, "reset", "--hard", "HEAD~1")
	r := inspectFixture(t, f)
	entry, e := TrashRepo(context.Background(), s, r, "leave", "", false)
	cleanupEntry(t, s, &entry)
	if e != nil {
		t.Fatal(e)
	}
	if e = Purge(context.Background(), s, &entry); e == nil || !strings.Contains(e.Error(), "recovery") {
		t.Fatalf("reflog-only commit lost: %v", e)
	}
	if e = Restore(context.Background(), s, &entry, ""); e != nil {
		t.Fatal(e)
	}
	mustGit(t, f.local, "reflog", "expire", "--expire=now", "--all")
	r = inspectFixture(t, f)
	next, e := TrashRepo(context.Background(), s, r, "leave", "", false)
	cleanupEntry(t, s, &next)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Rename(f.remote, f.remote+"-gone"); e != nil {
		t.Fatal(e)
	}
	if e = Purge(context.Background(), s, &next); e == nil {
		t.Fatal("unavailable remote must block purge")
	}
}
func TestInterruptedMoveRecovery(t *testing.T) {
	f := repoFixture(t)
	s := testStore(t, f)
	id := newID()
	stage := filepath.Join(f.root, ".git-shed-stage-"+id)
	from := filepath.Join(f.local, "file.txt")
	to := filepath.Join(stage, "checkout", "file.txt")
	entry := Entry{ID: id, Stage: stage, Original: f.local, State: "moving", Moves: []Move{{from, to}, {filepath.Join(f.local, ".git"), filepath.Join(stage, "checkout", ".git")}}}
	if e := s.save(&entry); e != nil {
		t.Fatal(e)
	}
	if e := renameNoClobber(from, to); e != nil {
		t.Fatal(e)
	}
	if e := Restore(context.Background(), s, &entry, ""); e != nil {
		t.Fatal(e)
	}
	if !exists(from) || !exists(filepath.Join(f.local, ".git")) {
		t.Fatal("interrupted restore failed")
	}
}
func TestSafePathsAndJournalIDs(t *testing.T) {
	root, err := canonical(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"../escape", "/absolute", "."} {
		if _, e := safePath(root, rel); e == nil {
			t.Fatal("accepted " + rel)
		}
	}
	if e := os.Symlink(t.TempDir(), filepath.Join(root, "link")); e != nil {
		t.Fatal(e)
	}
	if _, e := safePath(root, "link/file"); e == nil {
		t.Fatal("accepted symlink ancestor")
	}
	s := Store{root}
	if _, e := s.load("../../etc/passwd"); e == nil {
		t.Fatal("accepted invalid journal ID")
	}
}
func TestJournalLockAndOriginChange(t *testing.T) {
	f := repoFixture(t)
	s := testStore(t, f)
	unlock, e := s.lock()
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.lock(); e == nil {
		t.Fatal("concurrent lock accepted")
	}
	unlock()
	unlock, e = s.lock()
	if e != nil {
		t.Fatal(e)
	}
	unlock()
	r := inspectFixture(t, f)
	mustGit(t, f.local, "remote", "set-url", "origin", filepath.Join(f.root, "different.git"))
	if _, e = TrashRepo(context.Background(), s, r, "leave", "", false); e == nil || !strings.Contains(e.Error(), "changed") {
		t.Fatalf("changed origin must invalidate approval: %v", e)
	}
}
func TestPurgePreservesDeletedRemoteBranchHistory(t *testing.T) {
	f := repoFixture(t)
	s := testStore(t, f)
	mustGit(t, f.local, "switch", "-c", "remote-deleted")
	write(t, filepath.Join(f.local, "lost.txt"), "remote branch\n")
	mustGit(t, f.local, "add", ".")
	mustGit(t, f.local, "commit", "-m", "remote branch")
	mustGit(t, f.local, "push", "-u", "origin", "remote-deleted")
	mustGit(t, f.local, "switch", "main")
	mustGit(t, f.local, "branch", "-D", "remote-deleted")
	mustGit(t, f.remote, "update-ref", "-d", "refs/heads/remote-deleted")
	mustGit(t, f.local, "reflog", "expire", "--expire=now", "--all")
	r := inspectFixture(t, f)
	if r.LocalOnly != 0 || r.RecoveryCommits != 1 {
		t.Fatalf("stale remote ref history: %+v", r)
	}
	entry, e := TrashRepo(context.Background(), s, r, "leave", "", false)
	cleanupEntry(t, s, &entry)
	if e != nil {
		t.Fatal(e)
	}
	if e = Purge(context.Background(), s, &entry); e == nil {
		t.Fatal("purge lost deleted-remote branch history")
	}
}
func TestCanonicalDestinationAncestorAlias(t *testing.T) {
	root, err := canonical(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	target := filepath.Join(root, "target")
	if e := os.Mkdir(target, 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(target, alias); e != nil {
		t.Fatal(e)
	}
	got, e := canonicalDestination(filepath.Join(alias, "new", "checkout"))
	if e != nil || got != filepath.Join(target, "new", "checkout") {
		t.Fatal(got, e)
	}
	if _, e = canonicalDestination(alias); e == nil {
		t.Fatal("final destination symlink accepted")
	}
}
func TestLeaveEstimateExcludesPreservedFiles(t *testing.T) {
	f := repoFixture(t)
	s := testStore(t, f)
	write(t, filepath.Join(f.local, "cache", "large"), strings.Repeat("x", 1024*1024))
	r := inspectFixture(t, f)
	estimate, known := estimateLeave(r)
	if !known || estimate >= r.AllocatedBytes {
		t.Fatal("estimate includes preserved cache")
	}
	entry, e := TrashRepo(context.Background(), s, r, "leave", "", false)
	cleanupEntry(t, s, &entry)
	if e != nil {
		t.Fatal(e)
	}
	if delta := entry.Bytes - estimate; delta > 32768 || delta < -32768 {
		t.Fatalf("estimate %d differs materially from moved payload %d", estimate, entry.Bytes)
	}
}
