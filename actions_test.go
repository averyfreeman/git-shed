package main

import (
	"bufio"
	"context"
	tea "github.com/charmbracelet/bubbletea"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func autoActions() Actions {
	return Actions{In: bufio.NewReader(strings.NewReader("")), Out: &strings.Builder{}, Yes: true}
}
func TestPushExplicitBranchAndTagAndNoForce(t *testing.T) {
	f := repoFixture(t)
	mustGit(t, f.local, "switch", "-c", "feature")
	write(t, filepath.Join(f.local, "feature.txt"), "feature\n")
	mustGit(t, f.local, "add", ".")
	mustGit(t, f.local, "commit", "-m", "feature")
	mustGit(t, f.local, "tag", "v1")
	mustGit(t, f.local, "switch", "main")
	r := inspectFixture(t, f)
	a := autoActions()
	a.Branches = []string{"feature"}
	a.Tags = []string{"v1"}
	if e := Push(context.Background(), r, a); e != nil {
		t.Fatal(e)
	}
	r = inspectFixture(t, f)
	if r.LocalOnly != 0 || len(r.UnpublishedTags) > 0 {
		t.Fatal(r)
	}
	mustGit(t, f.local, "config", "remote.origin.mirror", "true")
	mustGit(t, f.local, "switch", "feature")
	mustGit(t, f.local, "reset", "--hard", "HEAD~1")
	r = Inspect(context.Background(), f.local, false, false)
	a.Tags = nil
	if e := Push(context.Background(), r, a); e == nil {
		t.Fatal("non-fast-forward push must not force")
	}
	if got := mustGit(t, f.remote, "rev-parse", "refs/heads/feature"); got == r.Head {
		t.Fatal("remote was overwritten")
	}
}
func TestCommitPushRetainsExistingIndex(t *testing.T) {
	f := repoFixture(t)
	write(t, filepath.Join(f.local, "staged.txt"), "staged\n")
	mustGit(t, f.local, "add", "staged.txt")
	write(t, filepath.Join(f.local, "selected.txt"), "selected\n")
	write(t, filepath.Join(f.local, "left.txt"), "left\n")
	r := Inspect(context.Background(), f.local, false, false)
	a := autoActions()
	a.Files = []string{"selected.txt"}
	a.Message = "reviewed files"
	if e := CommitPush(context.Background(), r, a); e != nil {
		t.Fatal(e)
	}
	names := mustGit(t, f.local, "show", "--format=", "--name-only", "HEAD")
	if !strings.Contains(names, "staged.txt") || !strings.Contains(names, "selected.txt") || strings.Contains(names, "left.txt") {
		t.Fatal(names)
	}
	if !exists(filepath.Join(f.local, "left.txt")) {
		t.Fatal("unselected work lost")
	}
}
func TestCommitHookAndPushFailuresPreserveWork(t *testing.T) {
	f := repoFixture(t)
	write(t, filepath.Join(f.local, ".git", "hooks", "pre-commit"), "#!/bin/sh\nexit 1\n")
	if e := os.Chmod(filepath.Join(f.local, ".git", "hooks", "pre-commit"), 0700); e != nil {
		t.Fatal(e)
	}
	write(t, filepath.Join(f.local, "new.txt"), "new\n")
	a := autoActions()
	a.Files = []string{"new.txt"}
	a.Message = "new"
	r := Inspect(context.Background(), f.local, false, false)
	head := r.Head
	if e := CommitPush(context.Background(), r, a); e == nil {
		t.Fatal("hook must fail")
	}
	if got := mustGit(t, f.local, "diff", "--cached", "--name-only"); got != "new.txt" {
		t.Fatal("staging lost")
	}
	if got := mustGit(t, f.local, "rev-parse", "HEAD"); got != head {
		t.Fatal("hook rejection created commit")
	}
	_ = os.Remove(filepath.Join(f.local, ".git", "hooks", "pre-commit"))
	mustGit(t, f.local, "remote", "set-url", "--push", "origin", filepath.Join(f.root, "no-remote"))
	r = Inspect(context.Background(), f.local, false, false)
	a.Files = nil
	if e := CommitPush(context.Background(), r, a); e == nil {
		t.Fatal("push should fail")
	}
	if got := mustGit(t, f.local, "rev-parse", "HEAD"); got == head {
		t.Fatal("failed push should retain successful commit")
	}
}
func fakeExecutable(t *testing.T, name, script string) {
	t.Helper()
	bin := t.TempDir()
	p := filepath.Join(bin, name)
	write(t, p, "#!/bin/sh\n"+script)
	if e := os.Chmod(p, 0700); e != nil {
		t.Fatal(e)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}
func TestGitHubCreationAndBrowserAreMocked(t *testing.T) {
	f := repoFixture(t)
	mustGit(t, f.local, "remote", "remove", "origin")
	log := filepath.Join(f.root, "gh.log")
	t.Setenv("FAKE_LOG", log)
	t.Setenv("FAKE_REMOTE", f.remote)
	fakeExecutable(t, "gh", "printf '%s\\n' \"$@\" > \"$FAKE_LOG\"\ngit remote add origin \"$FAKE_REMOTE\"\n")
	r := Inspect(context.Background(), f.local, false, false)
	a := autoActions()
	a.Owner = "example-owner"
	a.Visibility = "private"
	if e := CreateOrigin(context.Background(), r, a); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(log)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(b), "example-owner/local") || !strings.Contains(string(b), "--private") {
		t.Fatal(string(b))
	}
	a.Visibility = ""
	mustGit(t, f.local, "remote", "remove", "origin")
	r = Inspect(context.Background(), f.local, false, false)
	if e = CreateOrigin(context.Background(), r, a); e == nil {
		t.Fatal("owner and visibility must be explicit")
	}
	for _, exe := range []string{"open", "xdg-open"} {
		fakeExecutable(t, exe, "printf '%s\\n' \"$@\" > \"$FAKE_LOG\"\n")
	}
	if e = openURL(context.Background(), "https://github.com/example-owner?tab=repositories"); e != nil {
		t.Fatal(e)
	}
	b, _ = os.ReadFile(log)
	if !strings.Contains(string(b), "?tab=repositories") {
		t.Fatal(string(b))
	}
}
func TestCreateOriginWithInitialCommit(t *testing.T) {
	f := repoFixture(t)
	mustGit(t, f.root, "init", "--bare", filepath.Join(f.root, "initial.git"))
	initial := filepath.Join(f.root, "initial")
	mustGit(t, f.root, "init", "-b", "main", initial)
	write(t, filepath.Join(initial, "hello.txt"), "hello\n")
	t.Setenv("FAKE_REMOTE", filepath.Join(f.root, "initial.git"))
	fakeExecutable(t, "gh", "git remote add origin \"$FAKE_REMOTE\"\n")
	a := autoActions()
	a.Owner = "test"
	a.Visibility = "private"
	a.Message = "initial"
	a.Files = []string{"hello.txt"}
	r := Inspect(context.Background(), initial, false, false)
	if e := CreateOrigin(context.Background(), r, a); e != nil {
		t.Fatal(e)
	}
	r = Inspect(context.Background(), initial, false, true)
	if r.Head == "" || r.LocalOnly != 0 {
		t.Fatal(r)
	}
}
func TestDryRunSelectionAndCLIJSON(t *testing.T) {
	f := repoFixture(t)
	write(t, filepath.Join(f.local, "new.txt"), "new\n")
	before := mustGit(t, f.local, "rev-parse", "HEAD")
	var out, logs strings.Builder
	e := run(context.Background(), []string{"commit-push", "--yes", "--dry-run", "--message", "test", "--file", "new.txt", "--format", "json", f.local}, &out, &logs, strings.NewReader(""))
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(out.String(), `"status":"preview"`) {
		t.Fatal(out.String())
	}
	if mustGit(t, f.local, "rev-parse", "HEAD") != before || mustGit(t, f.local, "diff", "--cached", "--name-only") != "" {
		t.Fatal("dry-run changed work")
	}
	nested := filepath.Join(f.local, "nested")
	if e = os.Mkdir(nested, 0700); e != nil {
		t.Fatal(e)
	}
	if _, e = selectPaths([]string{f.local, nested}, ""); e == nil {
		t.Fatal("overlap accepted")
	}
	selection := filepath.Join(f.root, "selection.json")
	write(t, selection, `["`+f.local+`"]`)
	rows, e := selectPaths(nil, selection)
	if e != nil || len(rows) != 1 {
		t.Fatal(rows, e)
	}
}
func TestPickerFilterSortAndAction(t *testing.T) {
	p := newPicker([]Repo{{Path: "/a", Status: "no_origin", AllocatedBytes: 10}, {Path: "/b", Status: "synchronized", AllocatedBytes: 20}})
	m, _ := p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	p = m.(picker)
	if p.rows[0].Path != "/b" {
		t.Fatal("size sort")
	}
	p.filter = "no_origin"
	p.rebuild()
	if len(p.visible) != 1 {
		t.Fatal("filter")
	}
	m, _ = p.Update(tea.KeyMsg{Type: tea.KeySpace})
	p = m.(picker)
	m, _ = p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	p = m.(picker)
	if p.action != "trash" || len(p.chosen) != 1 || p.chosen[0] != "/a" {
		t.Fatal(p)
	}
}
func TestCloneRecordedRepositoryAndRefuseOverwrite(t *testing.T) {
	f := repoFixture(t)
	entry := Entry{Original: filepath.Join(f.root, "reclone"), Origin: f.remote, Branch: "main", Head: mustGit(t, f.local, "rev-parse", "HEAD")}
	a := autoActions()
	if e := Clone(context.Background(), entry, "", a); e != nil {
		t.Fatal(e)
	}
	if !exists(filepath.Join(entry.Original, ".git")) {
		t.Fatal("clone not created")
	}
	if e := Clone(context.Background(), entry, "", a); e == nil {
		t.Fatal("nonempty destination overwritten")
	}
}
func TestCloneMappedAndLocallyNamedBranches(t *testing.T) {
	f := repoFixture(t)
	head := mustGit(t, f.local, "rev-parse", "HEAD")
	for _, upstream := range []string{"origin/main", ""} {
		dest := filepath.Join(f.root, "clone-"+newID())
		entry := Entry{Original: dest, Origin: f.remote, Branch: "local-name", Head: head, Branches: []Branch{{Name: "local-name", Upstream: upstream}}}
		if e := Clone(context.Background(), entry, "", autoActions()); e != nil {
			t.Fatal(e)
		}
		if got := mustGit(t, dest, "symbolic-ref", "--short", "HEAD"); got != "local-name" {
			t.Fatal(got)
		}
	}
}
