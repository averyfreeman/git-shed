package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

func Scan(ctx context.Context, o ScanOptions) (Report, error) {
	root, e := canonical(o.Root)
	if e != nil {
		return Report{}, e
	}
	info, e := os.Stat(root)
	if e != nil || !info.IsDir() {
		return Report{}, fmt.Errorf("scan root must be a directory")
	}
	report := Report{Version: 1, Root: root, ScannedAt: time.Now().UTC().Format(time.RFC3339), Duplicates: map[string][]string{}, Summary: map[string]int64{}}
	var roots []string
	excludes := map[string]bool{}
	for _, s := range o.Excludes {
		excludes[s] = true
	}
	e = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("%s: %v", p, err))
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.Name() == ".git" {
			roots = append(roots, filepath.Dir(p))
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() && p != root && excludes[d.Name()] {
			report.Excluded = append(report.Excluded, p)
			return filepath.SkipDir
		}
		return nil
	})
	if e != nil {
		return report, e
	}
	roots = unique(roots)
	workers := o.Workers
	if workers < 1 {
		workers = 4
	}
	if workers > 32 {
		workers = 32
	}
	jobs := make(chan string)
	results := make(chan Repo)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range jobs {
				results <- Inspect(ctx, p, o.Sizes, o.Verify)
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, p := range roots {
			select {
			case jobs <- p:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() { wg.Wait(); close(results) }()
	done := 0
	for r := range results {
		done++
		if o.Progress != nil {
			o.Progress(done, len(roots), r)
		}
		if r.Kind == "invalid" {
			report.Invalid = append(report.Invalid, r.Path)
			report.Errors = append(report.Errors, r.Errors...)
		} else {
			report.Repositories = append(report.Repositories, r)
		}
	}
	sort.Slice(report.Repositories, func(i, j int) bool { return report.Repositories[i].Path < report.Repositories[j].Path })
	for i := range report.Repositories {
		r := &report.Repositories[i]
		for _, p := range roots {
			if p != r.Path && inside(r.Path, p) {
				r.Nested = append(r.Nested, p)
			}
		}
		if len(r.Nested) > 0 {
			r.Blockers = append(r.Blockers, "contains nested repositories")
		}
		if r.Origin != "" {
			key := normalizeOrigin(r.Origin)
			report.Duplicates[key] = append(report.Duplicates[key], r.Path)
		}
		report.Summary["repositories"]++
		report.Summary[r.Status]++
		if len(r.Changes) > 0 || len(r.Untracked) > 0 {
			report.Summary["dirty"]++
		}
		if r.LocalOnly > 0 {
			report.Summary["unpublished_commits"]++
		}
		if len(r.Blockers) == 0 {
			report.Summary["eligible_before_file_review"]++
		}
		parent := false
		for _, q := range report.Repositories {
			if q.Path != r.Path && inside(q.Path, r.Path) {
				parent = true
				break
			}
		}
		if !parent {
			report.Summary["checkout_bytes"] += r.Bytes
			report.Summary["checkout_allocated_bytes"] += r.AllocatedBytes
		}
	}
	for k, v := range report.Duplicates {
		if len(v) < 2 {
			delete(report.Duplicates, k)
		}
	}
	report.Summary["invalid_markers"] = int64(len(report.Invalid))
	return report, ctx.Err()
}
func Inspect(ctx context.Context, path string, sizes, verify bool) Repo {
	if p, e := canonical(path); e == nil {
		path = p
	}
	r := Repo{Path: path, Kind: "checkout", Verification: "cached", Status: "unknown", Tags: map[string]string{}, LocalOnly: -1}
	get := func(args ...string) string {
		s, e := git(ctx, path, args...)
		if e != nil {
			r.Errors = append(r.Errors, e.Error())
		}
		return strings.TrimSpace(s)
	}
	top, e := git(ctx, path, "rev-parse", "--show-toplevel")
	if e != nil || strings.TrimSpace(top) != path {
		r.Kind = "invalid"
		r.Blockers = append(r.Blockers, "invalid checkout")
		r.Errors = append(r.Errors, fmt.Sprintf("%s: invalid or inaccessible checkout: %v", path, e))
		return r
	}
	if s, _ := git(ctx, path, "remote", "get-url", "origin"); s != "" {
		r.Origin = strings.TrimSpace(s)
	}
	if s, _ := git(ctx, path, "remote", "get-url", "--push", "--all", "origin"); s != "" {
		r.PushURLs = lines(s)
	}
	r.Head = strings.TrimSpace(optionalGit(ctx, path, "rev-parse", "--verify", "HEAD"))
	r.Branch = strings.TrimPrefix(strings.TrimSpace(optionalGit(ctx, path, "symbolic-ref", "HEAD")), "refs/heads/")
	r.Tracked = splitZero(rawGet(ctx, path, &r, "ls-files", "--cached", "-z"))
	r.Tracked = unique(r.Tracked)
	for _, entry := range splitZero(rawGet(ctx, path, &r, "ls-files", "-v", "-z")) {
		if len(entry) > 1 && (entry[0] == 'S' || entry[0] == 's' || entry[0] >= 'a' && entry[0] <= 'z') {
			r.Blockers = append(r.Blockers, "skip-worktree or assume-unchanged files require review")
			break
		}
	}
	r.Untracked = splitZero(rawGet(ctx, path, &r, "ls-files", "--others", "--exclude-standard", "-z"))
	parseStatus(rawGet(ctx, path, &r, "status", "--porcelain=v1", "-z", "--untracked-files=normal", "--ignored=matching"), &r)
	for _, line := range lines(get("for-each-ref", "--format=%(refname:strip=2)%09%(objectname)%09%(upstream)", "refs/heads")) {
		p := strings.Split(line, "\t")
		if len(p) < 2 {
			continue
		}
		b := Branch{Name: p[0], OID: p[1], Ahead: -1, Behind: -1, Status: "missing_upstream"}
		if len(p) > 2 {
			b.Upstream = strings.TrimPrefix(p[2], "refs/remotes/")
		}
		if b.Upstream != "" {
			setBranchCounts(ctx, path, &b, b.OID, b.Upstream)
		}
		r.Branches = append(r.Branches, b)
	}
	for _, line := range lines(get("for-each-ref", "--format=%(refname:strip=2)%09%(objectname)", "refs/tags")) {
		p := strings.SplitN(line, "\t", 2)
		if len(p) == 2 {
			r.Tags[p[0]] = p[1]
		}
	}
	r.Stashes = len(lines(get("stash", "list", "--format=%H")))
	s, e := git(ctx, path, "rev-list", "--count", "--reflog", "--not", "--all", "HEAD")
	if e == nil {
		r.RecoveryCommits, _ = strconv.Atoi(strings.TrimSpace(s))
	}
	marker, e := os.Lstat(filepath.Join(path, ".git"))
	if e != nil {
		r.Errors = append(r.Errors, e.Error())
	} else if !marker.IsDir() {
		r.Kind = "linked_or_submodule"
		r.Blockers = append(r.Blockers, "external .git metadata (worktree or submodule)")
	}
	if s := strings.TrimSpace(optionalGit(ctx, path, "rev-parse", "--show-superproject-working-tree")); s != "" {
		r.Kind = "submodule"
		r.Blockers = append(r.Blockers, "registered submodule")
	}
	if len(lines(get("worktree", "list", "--porcelain"))) > 3 { // Count worktree records rather than record fields.
		s := optionalGit(ctx, path, "worktree", "list", "--porcelain")
		if strings.Count(s, "worktree ") > 1 {
			r.Blockers = append(r.Blockers, "linked worktrees share metadata")
		}
	}
	if get("rev-parse", "--is-shallow-repository") == "true" {
		r.Blockers = append(r.Blockers, "shallow repository")
	}
	for _, ref := range lines(get("for-each-ref", "--format=%(refname)")) {
		if !strings.HasPrefix(ref, "refs/heads/") && !strings.HasPrefix(ref, "refs/tags/") && !strings.HasPrefix(ref, "refs/remotes/") && ref != "refs/stash" {
			r.Blockers = append(r.Blockers, "custom refs require manual preservation")
			break
		}
	}
	gitDir := get("rev-parse", "--absolute-git-dir")
	if _, e := os.Stat(filepath.Join(gitDir, "info", "grafts")); e == nil {
		r.Blockers = append(r.Blockers, "legacy grafts require manual preservation")
	}
	if s := optionalGit(ctx, path, "config", "--get", "core.worktree"); s != "" {
		r.Blockers = append(r.Blockers, "custom core.worktree requires manual cleanup")
	}
	if _, e := os.Stat(filepath.Join(gitDir, "objects", "info", "alternates")); e == nil {
		r.Blockers = append(r.Blockers, "external object store")
	}
	if usesLFS(ctx, path, r.Tracked) {
		r.Blockers = append(r.Blockers, "Git LFS requires separate object verification")
	}
	if s := optionalGit(ctx, path, "ls-files", "--stage"); strings.Contains(s, "160000 ") {
		r.Blockers = append(r.Blockers, "contains registered submodules")
	}
	for _, name := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply", "sequencer", "index.lock"} {
		s := strings.TrimSpace(optionalGit(ctx, path, "rev-parse", "--git-path", name))
		if !filepath.IsAbs(s) {
			s = filepath.Join(path, s)
		}
		if _, e := os.Lstat(s); e == nil {
			r.Blockers = append(r.Blockers, "Git operation in progress: "+name)
		}
	}
	if r.Origin == "" {
		r.Status = "no_origin"
		r.Verification = "no_origin"
		r.Blockers = append(r.Blockers, "no origin")
	} else if verify {
		verifyRemote(ctx, &r)
	} else {
		cachedCoverage(ctx, &r)
		r.Status = currentStatus(r)
		r.Blockers = append(r.Blockers, "fresh origin verification required")
	}
	if len(r.Changes) > 0 {
		r.Blockers = append(r.Blockers, "tracked changes need review and commit")
	}
	if r.Head == "" {
		r.Blockers = append(r.Blockers, "no HEAD commit")
	}
	if r.Stashes > 0 {
		r.Blockers = append(r.Blockers, "stashes need preservation")
	}
	if sizes {
		r.Bytes, r.AllocatedBytes, e = treeSize(ctx, path)
		r.SizeKnown = e == nil
		r.IgnoredSizes = measurePaths(ctx, path, r.Ignored, &r)
		r.UntrackedSizes = measurePaths(ctx, path, r.UntrackedRoots, &r)
		if e != nil {
			r.Errors = append(r.Errors, e.Error())
		}
	}
	// Avoid reporting a nested Git marker as an ignored disposable build directory.
	_ = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			r.Errors = append(r.Errors, fmt.Sprintf("%s: %v", p, err))
			return filepath.SkipDir
		}
		if p == filepath.Join(path, ".git") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if p != path && d.Name() == ".git" {
			r.Nested = append(r.Nested, filepath.Dir(p))
			if d.IsDir() {
				return filepath.SkipDir
			}
		}
		if p == filepath.Join(path, ".git") && d.IsDir() {
			return filepath.SkipDir
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return nil
	})
	if len(r.Nested) > 0 {
		r.Blockers = append(r.Blockers, "contains nested repositories")
	}
	if len(r.Errors) > 0 {
		r.Blockers = append(r.Blockers, "inspection errors")
	}
	r.Blockers = unique(r.Blockers)
	fp := struct {
		Head, Origin, Kind         string
		PushURLs                   []string
		Branches                   []Branch
		Tags                       map[string]string
		Changes                    []Change
		Untracked, Ignored, Nested []string
		Stashes                    int
	}{r.Head, r.Origin, r.Kind, r.PushURLs, localBranches(r.Branches), r.Tags, r.Changes, r.Untracked, r.Ignored, r.Nested, r.Stashes}
	b, _ := json.Marshal(fp)
	r.Fingerprint = sumString(string(b))
	return r
}
func optionalGit(ctx context.Context, p string, a ...string) string {
	s, _ := git(ctx, p, a...)
	return s
}
func rawGet(ctx context.Context, p string, r *Repo, a ...string) string {
	s, e := git(ctx, p, a...)
	if e != nil {
		r.Errors = append(r.Errors, e.Error())
	}
	return s
}
func parseStatus(s string, r *Repo) {
	parts := splitZero(s)
	for i := 0; i < len(parts); i++ {
		s := parts[i]
		if len(s) < 3 {
			r.Errors = append(r.Errors, "malformed Git status")
			continue
		}
		c, p := s[:2], s[3:]
		if c == "!!" {
			r.Ignored = append(r.Ignored, p)
			continue
		}
		if c == "??" {
			r.UntrackedRoots = append(r.UntrackedRoots, p)
			continue
		}
		v := Change{Code: c, Path: p}
		if strings.ContainsAny(c, "RC") && i+1 < len(parts) {
			i++
			v.From = parts[i]
		}
		r.Changes = append(r.Changes, v)
	}
}
func setBranchCounts(ctx context.Context, p string, b *Branch, l, r string) {
	s, e := git(ctx, p, "rev-list", "--left-right", "--count", l+"..."+r)
	if e != nil {
		b.Status = "missing_remote_branch"
		return
	}
	fmt.Sscanf(s, "%d %d", &b.Ahead, &b.Behind)
	switch {
	case b.Ahead == 0 && b.Behind == 0:
		b.Status = "synchronized"
	case b.Ahead > 0 && b.Behind == 0:
		b.Status = "ahead"
	case b.Ahead == 0 && b.Behind > 0:
		b.Status = "behind"
	default:
		b.Status = "diverged"
	}
}
func currentStatus(r Repo) string {
	if r.Origin == "" {
		return "no_origin"
	}
	if r.Head == "" {
		return "unborn"
	}
	if r.Branch == "" {
		return "detached"
	}
	for _, b := range r.Branches {
		if b.Name == r.Branch {
			return b.Status
		}
	}
	return "unknown"
}
func cachedCoverage(ctx context.Context, r *Repo) {
	s, e := git(ctx, r.Path, "for-each-ref", "--format=%(refname)", "refs/remotes/origin")
	if e != nil || s == "" {
		return
	}
	args := []string{"rev-list", "--count", "HEAD"}
	for _, b := range r.Branches {
		args = append(args, b.OID)
	}
	args = append(args, "--not")
	args = append(args, lines(s)...)
	s, e = git(ctx, r.Path, args...)
	if e == nil {
		r.LocalOnly, _ = strconv.Atoi(strings.TrimSpace(s))
	}
}
func treeSize(ctx context.Context, p string) (int64, int64, error) {
	var bytes, alloc int64
	seen := map[string]bool{}
	e := filepath.WalkDir(p, func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		i, e := d.Info()
		if e != nil {
			return e
		}
		v := reflect.ValueOf(i.Sys())
		if v.Kind() == reflect.Pointer {
			v = v.Elem()
		}
		key := ""
		if v.IsValid() && v.Kind() == reflect.Struct {
			dev, ino, blocks := v.FieldByName("Dev"), v.FieldByName("Ino"), v.FieldByName("Blocks")
			if dev.IsValid() && ino.IsValid() {
				key = fmt.Sprint(dev.Interface(), "/", ino.Interface())
			}
			if seen[key] && key != "" {
				return nil
			}
			if blocks.IsValid() {
				alloc += blocks.Int() * 512
			} else {
				alloc += i.Size()
			}
		}
		if key != "" {
			seen[key] = true
		}
		if !d.IsDir() {
			bytes += i.Size()
		}
		return nil
	})
	return bytes, alloc, e
}
func usesLFS(ctx context.Context, path string, tracked []string) bool {
	if len(tracked) == 0 {
		return false
	}
	// check-attr reads attributes, including global attribute files, without requiring git-lfs.
	for start := 0; start < len(tracked); start += 200 {
		end := start + 200
		if end > len(tracked) {
			end = len(tracked)
		}
		args := []string{"check-attr", "-z", "filter", "--"}
		args = append(args, tracked[start:end]...)
		s, e := git(ctx, path, args...)
		if e != nil {
			return true
		}
		parts := splitZero(s)
		for i := 2; i < len(parts); i += 3 {
			if parts[i] == "lfs" {
				return true
			}
		}
	}
	return false
}
func measurePaths(ctx context.Context, root string, paths []string, r *Repo) map[string]int64 {
	out := map[string]int64{}
	for _, rel := range paths {
		p, e := safePath(root, strings.TrimSuffix(rel, "/"))
		if e != nil {
			r.Errors = append(r.Errors, e.Error())
			continue
		}
		_, allocated, e := treeSize(ctx, p)
		if e != nil {
			r.Errors = append(r.Errors, e.Error())
			continue
		}
		out[rel] = allocated
	}
	return out
}
