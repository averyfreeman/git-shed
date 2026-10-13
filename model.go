package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

type Branch struct {
	Name     string `json:"name"`
	OID      string `json:"oid"`
	Upstream string `json:"upstream,omitempty"`
	Ahead    int    `json:"ahead"`
	Behind   int    `json:"behind"`
	Status   string `json:"status"`
}
type Change struct {
	Code string `json:"code"`
	Path string `json:"path"`
	From string `json:"from,omitempty"`
}
type Repo struct {
	Path                string            `json:"path"`
	Origin              string            `json:"origin,omitempty"`
	PushURLs            []string          `json:"push_urls,omitempty"`
	Head                string            `json:"head,omitempty"`
	Branch              string            `json:"branch,omitempty"`
	Branches            []Branch          `json:"branches"`
	Tags                map[string]string `json:"tags"`
	Status              string            `json:"status"`
	Verification        string            `json:"verification"`
	VerifiedAt          string            `json:"verified_at,omitempty"`
	LocalOnly           int               `json:"local_only_commits"`
	UnpublishedTags     []string          `json:"unpublished_tags,omitempty"`
	Changes             []Change          `json:"changes"`
	Untracked           []string          `json:"untracked,omitempty"`
	IgnoredSizes        map[string]int64  `json:"ignored_allocated_bytes,omitempty"`
	UntrackedSizes      map[string]int64  `json:"untracked_allocated_bytes,omitempty"`
	Ignored             []string          `json:"ignored,omitempty"`
	TrackedFiles        int               `json:"tracked_files"`
	UntrackedRoots      []string          `json:"untracked_roots,omitempty"`
	Tracked             []string          `json:"tracked,omitempty"`
	Stashes             int               `json:"stashes"`
	RecoveryCommits     int               `json:"recovery_commits"`
	Bytes               int64             `json:"bytes"`
	AllocatedBytes      int64             `json:"allocated_bytes"`
	EstimatedLeaveBytes int64             `json:"estimated_leave_reclaimable_bytes"`
	LeaveEstimateKnown  bool              `json:"leave_estimate_known"`
	SizeKnown           bool              `json:"size_known"`
	Kind                string            `json:"kind"`
	Nested              []string          `json:"nested,omitempty"`
	Blockers            []string          `json:"blockers"`
	Errors              []string          `json:"errors,omitempty"`
	FileSnapshot        string            `json:"file_snapshot,omitempty"`
	Fingerprint         string            `json:"fingerprint"`
}
type ScanOptions struct {
	Root     string
	Workers  int
	Verify   bool
	Sizes    bool
	Excludes []string
	Progress func(int, int, Repo)
}
type Report struct {
	Version      int                 `json:"version"`
	Root         string              `json:"root"`
	ScannedAt    string              `json:"scanned_at"`
	Repositories []Repo              `json:"repositories"`
	Invalid      []string            `json:"invalid_git_markers,omitempty"`
	Excluded     []string            `json:"excluded_directories,omitempty"`
	Errors       []string            `json:"errors,omitempty"`
	Duplicates   map[string][]string `json:"duplicate_origins"`
	Summary      map[string]int64    `json:"summary"`
}

var commandTimeout = 90 * time.Second

func command(ctx context.Context, dir, executable string, args ...string) (string, error) {
	return commandInput(ctx, dir, executable, "", args...)
}
func commandInput(ctx context.Context, dir, executable, input string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	c := exec.CommandContext(ctx, executable, args...)
	c.Dir = dir
	if input != "" {
		c.Stdin = strings.NewReader(input)
	}
	c.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
	c.WaitDelay = 2 * time.Second
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error {
		if c.Process == nil {
			return os.ErrProcessDone
		}
		e := syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
		if e == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return e
	}
	out, err := c.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("%s: %w", executable, ctx.Err())
		}
		return "", fmt.Errorf("%s: %w: %s", executable, err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
func git(ctx context.Context, path string, args ...string) (string, error) {
	return command(ctx, "", "git", append([]string{"-C", path}, args...)...)
}
func splitZero(s string) []string {
	parts := strings.Split(s, "\x00")
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
func lines(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
func sumString(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func unique(a []string) []string {
	sort.Strings(a)
	out := a[:0]
	for _, s := range a {
		if len(out) == 0 || out[len(out)-1] != s {
			out = append(out, s)
		}
	}
	return out
}
func canonical(path string) (string, error) {
	p, e := filepath.Abs(path)
	if e != nil {
		return "", e
	}
	return filepath.EvalSymlinks(p)
}
func inside(root, path string) bool {
	r, e := filepath.Rel(root, path)
	return e == nil && r != ".." && !strings.HasPrefix(r, ".."+string(os.PathSeparator))
}
func normalizeOrigin(s string) string {
	s = redact(s)
	s = strings.TrimSuffix(strings.TrimSuffix(s, "/"), ".git")
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimPrefix(s, "ssh://")
	s = strings.TrimPrefix(s, "git@")
	if strings.HasPrefix(s, "github.com:") {
		s = strings.Replace(s, "github.com:", "github.com/", 1)
	}
	if strings.HasPrefix(strings.ToLower(s), "github.com/") {
		s = strings.ToLower(s)
	}
	return s
}
func localBranches(b []Branch) []Branch {
	out := append([]Branch(nil), b...)
	for i := range out {
		out[i].Ahead = 0
		out[i].Behind = 0
		out[i].Status = ""
	}
	return out
}

// Resolve existing ancestor aliases (such as /tmp -> /private/tmp on macOS)
// before validating a destination that may not exist yet.
func canonicalDestination(path string) (string, error) {
	p, e := filepath.Abs(path)
	if e != nil {
		return "", e
	}
	base := p
	var tail []string
	for {
		info, e := os.Lstat(base)
		if e == nil {
			if base == p && info.Mode()&os.ModeSymlink != 0 {
				return "", fmt.Errorf("destination is a symbolic link: %s", p)
			}
			if len(tail) > 0 && !info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
				return "", fmt.Errorf("destination ancestor is not a directory: %s", base)
			}
			resolved, e := filepath.EvalSymlinks(base)
			if e != nil {
				return "", e
			}
			for i := len(tail) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, tail[i])
			}
			return resolved, nil
		}
		if !os.IsNotExist(e) {
			return "", e
		}
		tail = append(tail, filepath.Base(base))
		parent := filepath.Dir(base)
		if parent == base {
			return "", e
		}
		base = parent
	}
}
