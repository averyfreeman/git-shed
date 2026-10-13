package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

type Actions struct {
	In         *bufio.Reader
	Out        io.Writer
	Yes        bool
	DryRun     bool
	Branches   []string
	Tags       []string
	Files      []string
	Message    string
	Mode       string
	Backup     string
	Discard    bool
	Owner      string
	Visibility string
}
type Result struct {
	Target  string `json:"target"`
	Action  string `json:"action"`
	Status  string `json:"status"`
	Detail  string `json:"detail,omitempty"`
	EntryID string `json:"entry_id,omitempty"`
}

func (a Actions) ask(prompt string) (string, error) {
	fmt.Fprint(a.Out, prompt+" ")
	s, e := a.In.ReadString('\n')
	if e != nil {
		return "", e
	}
	return strings.TrimSpace(s), nil
}
func (a Actions) confirm(prompt string) error {
	if a.DryRun {
		return nil
	}
	if a.Yes {
		return nil
	}
	s, e := a.ask(prompt + " Type yes to continue:")
	if e != nil {
		return fmt.Errorf("confirmation required (use --yes for noninteractive operation): %w", e)
	}
	if s != "yes" {
		return fmt.Errorf("cancelled")
	}
	return nil
}
func mutation(ctx context.Context, p string, exe string, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	c := exec.CommandContext(ctx, exe, args...)
	c.Dir = p
	c.Stdin = os.Stdin
	c.WaitDelay = 2 * time.Second
	output, e := c.CombinedOutput()
	if len(output) > 0 {
		fmt.Fprint(os.Stderr, redactText(string(output)))
	}
	return e
}
func pushSpecs(ctx context.Context, r Repo, a Actions) ([]string, error) {
	if r.Origin == "" {
		return nil, fmt.Errorf("no origin")
	}
	if len(r.PushURLs) != 1 {
		return nil, fmt.Errorf("exactly one origin push URL is required")
	}
	var specs []string
	branches := a.Branches
	if len(branches) == 0 {
		if a.Yes {
			if r.Branch == "" {
				return nil, fmt.Errorf("detached HEAD; choose branches explicitly")
			}
			branches = []string{r.Branch}
		} else {
			fmt.Fprintln(a.Out, "Branches:")
			for _, b := range r.Branches {
				fmt.Fprintf(a.Out, "  %s → %s (%s, +%d/-%d)\n", b.Name, b.Upstream, b.Status, b.Ahead, b.Behind)
			}
			s, e := a.ask("Branches to push (comma-separated; empty = current branch):")
			if e != nil {
				return nil, e
			}
			if s == "" {
				if r.Branch == "" {
					return nil, fmt.Errorf("select a named branch")
				}
				branches = []string{r.Branch}
			} else {
				branches = commaList(s)
			}
		}
	}
	if r.Head == "" && r.Branch != "" && len(r.Branches) == 0 {
		r.Branches = append(r.Branches, Branch{Name: r.Branch})
	}
	for _, name := range branches {
		found := false
		for _, b := range r.Branches {
			if b.Name == name {
				found = true
				dest := name
				if b.Upstream != "" {
					if !strings.HasPrefix(b.Upstream, "origin/") {
						return nil, fmt.Errorf("branch %s tracks another remote; configure origin tracking explicitly", name)
					}
					dest = strings.TrimPrefix(b.Upstream, "origin/")
				}
				if _, e := git(ctx, r.Path, "check-ref-format", "refs/heads/"+dest); e != nil {
					return nil, e
				}
				specs = append(specs, "refs/heads/"+name+":refs/heads/"+dest)
			}
		}
		if !found {
			return nil, fmt.Errorf("unknown branch %q", name)
		}
	}
	tags := a.Tags
	if !a.Yes && len(tags) == 0 && len(r.UnpublishedTags) > 0 {
		s, e := a.ask("Unpublished tags: " + strings.Join(r.UnpublishedTags, ", ") + ". Tags to push (comma-separated; empty = none):")
		if e != nil {
			return nil, e
		}
		tags = commaList(s)
	}
	for _, name := range tags {
		if _, ok := r.Tags[name]; !ok {
			return nil, fmt.Errorf("unknown tag %q", name)
		}
		specs = append(specs, "refs/tags/"+name+":refs/tags/"+name)
	}
	return specs, nil
}
func Push(ctx context.Context, r Repo, a Actions) error {
	specs, e := pushSpecs(ctx, r, a)
	if e != nil {
		return e
	}
	fmt.Fprintf(a.Out, "%s\nPush to %s:\n  %s\n", r.Path, redact(r.PushURLs[0]), strings.Join(specs, "\n  "))
	if e = a.confirm("Push these refs?"); e != nil {
		return e
	}
	if a.DryRun {
		return nil
	}
	fresh := Inspect(ctx, r.Path, false, false)
	if fresh.Fingerprint != r.Fingerprint {
		return fmt.Errorf("repository changed after push review")
	}
	// Explicit refspecs ignore push.default and push.followTags; no force or mirror flags.
	args := append([]string{"-c", "push.followTags=false", "-c", "remote.origin.mirror=false", "push", "--porcelain", "--set-upstream", "origin"}, specs...)
	if e = mutation(ctx, r.Path, "git", args...); e != nil {
		return fmt.Errorf("push failed; any local commits remain intact: %w", e)
	}
	checked := Inspect(ctx, r.Path, false, true)
	if checked.Verification != "verified" {
		return fmt.Errorf("push completed but origin verification failed")
	}
	for _, spec := range specs {
		parts := strings.SplitN(spec, ":", 2)
		local := strings.TrimSpace(optionalGit(ctx, r.Path, "rev-parse", parts[0]))
		m, e := advertised(ctx, r.Origin)
		if e != nil {
			return e
		}
		if m[parts[1]] != local {
			return fmt.Errorf("pushed ref %s is not confirmed on origin fetch URL", parts[1])
		}
	}
	return nil
}
func CommitPush(ctx context.Context, r Repo, a Actions) error {
	if r.Kind != "checkout" {
		return fmt.Errorf("commit-push requires a normal checkout")
	}
	for _, b := range r.Blockers {
		if strings.HasPrefix(b, "Git operation in progress:") {
			return fmt.Errorf("%s", b)
		}
	}
	fmt.Fprintf(a.Out, "\n%s\n", r.Path)
	for _, c := range r.Changes {
		fmt.Fprintf(a.Out, "%s %s\n", c.Code, c.Path)
	}
	for _, p := range r.UntrackedRoots {
		fmt.Fprintf(a.Out, "?? %s\n", p)
	}
	fmt.Fprintf(a.Out, "%d untracked files; select individual paths below.\n", len(r.Untracked))
	diffArgs := []string{"diff", "--no-ext-diff", "--no-textconv"}
	if r.Head != "" {
		diffArgs = append(diffArgs, "HEAD")
	}
	diffArgs = append(diffArgs, "--")
	diff, e := git(ctx, r.Path, diffArgs...)
	if e != nil {
		return e
	}
	fmt.Fprintln(a.Out, diff)
	files := a.Files
	if !a.Yes && len(files) == 0 {
		s, e := a.ask("Files to stage (JSON array; empty keeps existing staging):")
		if e != nil {
			return e
		}
		if s != "" {
			if e = json.Unmarshal([]byte(s), &files); e != nil {
				return fmt.Errorf("file selection must be a JSON array: %w", e)
			}
		}
	}
	for _, rel := range files {
		if _, e = safePath(r.Path, rel); e != nil {
			return e
		}
		allowed := false
		for _, p := range r.Tracked {
			if rel == p {
				allowed = true
			}
		}
		for _, p := range r.Untracked {
			if rel == p {
				allowed = true
			}
		}
		if !allowed {
			return fmt.Errorf("select an individual tracked or untracked file, not ignored files or directories: %s", rel)
		}
	}
	message := a.Message
	if message == "" {
		if a.Yes {
			return fmt.Errorf("--message is required for noninteractive commits")
		}
		message, e = a.ask("Commit message:")
		if e != nil {
			return e
		}
	}
	if strings.TrimSpace(message) == "" {
		return fmt.Errorf("commit message must not be empty")
	}
	staged := optionalGit(ctx, r.Path, "diff", "--cached", "--name-status", "--no-ext-diff", "--no-textconv")
	fmt.Fprintf(a.Out, "Existing staging (retained):\n%s\nAdditional files: %q\nMessage: %s\n", staged, files, message)
	// Resolve and review push destinations before creating any commit.
	specs, e := pushSpecs(ctx, r, a)
	if e != nil {
		return e
	}
	fmt.Fprintf(a.Out, "After commit, push to %s: %s\n", redact(r.PushURLs[0]), strings.Join(specs, ", "))
	if e = a.confirm("Stage, commit, and push this repository?"); e != nil {
		return e
	}
	if a.DryRun {
		return nil
	}
	fresh := Inspect(ctx, r.Path, false, false)
	if fresh.Fingerprint != r.Fingerprint {
		return fmt.Errorf("repository changed after commit review")
	}
	if len(files) > 0 {
		args := append([]string{"--literal-pathspecs", "add", "--"}, files...)
		if e = mutation(ctx, r.Path, "git", args...); e != nil {
			return e
		}
	}
	fmt.Fprintln(a.Out, "Final staged diff:")
	s, e := git(ctx, r.Path, "diff", "--cached", "--no-ext-diff", "--no-textconv")
	if e != nil {
		return e
	}
	fmt.Fprintln(a.Out, s)
	if e = a.confirm("Commit the final staged diff?"); e != nil {
		return e
	}
	if e = mutation(ctx, r.Path, "git", "commit", "-m", message); e != nil {
		return fmt.Errorf("commit failed; staging is retained: %w", e)
	}
	args := append([]string{"-c", "push.followTags=false", "-c", "remote.origin.mirror=false", "push", "--porcelain", "--set-upstream", "origin"}, specs...)
	if e = mutation(ctx, r.Path, "git", args...); e != nil {
		return fmt.Errorf("commit succeeded but push failed; local commit is retained: %w", e)
	}
	checked := Inspect(ctx, r.Path, false, true)
	if checked.Verification != "verified" {
		return fmt.Errorf("commit/push finished, but origin verification failed")
	}
	for _, spec := range specs {
		parts := strings.SplitN(spec, ":", 2)
		oid := strings.TrimSpace(optionalGit(ctx, r.Path, "rev-parse", parts[0]))
		m, e := advertised(ctx, r.Origin)
		if e != nil {
			return e
		}
		if m[parts[1]] != oid {
			return fmt.Errorf("pushed ref %s is not confirmed on origin", parts[1])
		}
	}
	if checked.LocalOnly > 0 {
		fmt.Fprintf(a.Out, "%d other local commits remain unpublished; inspect scan --verify before cleanup.\n", checked.LocalOnly)
	}
	return nil
}
func CreateOrigin(ctx context.Context, r Repo, a Actions) error {
	if r.Origin != "" {
		return fmt.Errorf("origin already exists")
	}
	if r.Kind != "checkout" {
		return fmt.Errorf("requires an ordinary checkout")
	}
	owner, visibility := a.Owner, a.Visibility
	name := filepath.Base(r.Path)
	var e error
	if owner == "" {
		if a.Yes {
			return fmt.Errorf("--owner is required")
		}
		owner, e = a.ask("GitHub owner (account or organization):")
		if e != nil {
			return e
		}
	}
	if visibility == "" {
		if a.Yes {
			return fmt.Errorf("--visibility private or public is required")
		}
		visibility, e = a.ask("Visibility (private/public):")
		if e != nil {
			return e
		}
	}
	if visibility != "private" && visibility != "public" {
		return fmt.Errorf("visibility must be private or public")
	}
	if !a.Yes {
		s, e := a.ask("Repository name (empty = " + name + "):")
		if e != nil {
			return e
		}
		if s != "" {
			name = s
		}
	}
	if !validGitHubName(owner) || !validGitHubName(name) {
		return fmt.Errorf("owner and name must use letters, digits, dots, underscores, or hyphens")
	}
	target := owner + "/" + name
	fmt.Fprintf(a.Out, "Create %s (%s), attach origin to %s.\n", target, visibility, r.Path)
	if e = a.confirm("Create this GitHub repository?"); e != nil {
		return e
	}
	if a.DryRun {
		return nil
	}
	// gh refuses an existing repository; do not attach one silently.
	if e = mutation(ctx, r.Path, "gh", "repo", "create", target, "--"+visibility, "--source", r.Path, "--remote", "origin"); e != nil {
		return e
	}
	fresh := Inspect(ctx, r.Path, false, false)
	if fresh.Head == "" {
		return CommitPush(ctx, fresh, a)
	}
	if len(fresh.Changes) > 0 || len(fresh.Untracked) > 0 {
		return CommitPush(ctx, fresh, a)
	}
	return Push(ctx, fresh, a)
}
func validGitHubName(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._-", r)) {
			return false
		}
	}
	return true
}
func browserURL(origin string) (string, error) {
	s := normalizeOrigin(redact(origin))
	parts := strings.Split(s, "/")
	if len(parts) >= 3 && (parts[0] == "github.com" || parts[0] == "gitlab.com" || parts[0] == "bitbucket.org") {
		return "https://" + s, nil
	}
	return "", fmt.Errorf("cannot infer repository web page; supported hosts: GitHub, GitLab.com, Bitbucket.org")
}
func openURL(ctx context.Context, url string) error {
	exe := "xdg-open"
	if runtime.GOOS == "darwin" {
		exe = "open"
	}
	_, e := command(ctx, "", exe, url)
	return e
}
func Clone(ctx context.Context, e Entry, dest string, a Actions) error {
	if dest == "" {
		dest = e.Original
	}
	p, err := canonicalDestination(dest)
	if err != nil {
		return err
	}
	if exists(p) {
		ds, err := os.ReadDir(p)
		if err != nil || len(ds) > 0 {
			return fmt.Errorf("clone destination must be empty; use --dest for a fresh directory")
		}
	}
	if err = safeAncestors(p); err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "Clone %s into %s; recorded branch %s, HEAD %s.\n", redact(e.Origin), p, e.Branch, e.Head)
	if err = a.confirm("Clone?"); err != nil {
		return err
	}
	if a.DryRun {
		return nil
	}
	refs, err := advertised(ctx, e.Origin)
	if err != nil {
		return err
	}
	remoteBranch := e.Branch
	for _, b := range e.Branches {
		if b.Name == e.Branch && strings.HasPrefix(b.Upstream, "origin/") {
			remoteBranch = strings.TrimPrefix(b.Upstream, "origin/")
		}
	}
	_, hasBranch := refs["refs/heads/"+remoteBranch]
	args := []string{"clone"}
	if e.Branch != "" && hasBranch {
		args = append(args, "--branch", remoteBranch)
	}
	args = append(args, "--", e.Origin, p)
	if err = mutation(ctx, "", "git", args...); err != nil {
		return err
	}
	if e.Branch == "" {
		return mutation(ctx, p, "git", "switch", "--detach", e.Head)
	}
	if !hasBranch {
		return mutation(ctx, p, "git", "switch", "-c", e.Branch, e.Head)
	}
	if remoteBranch != e.Branch {
		return mutation(ctx, p, "git", "branch", "-m", e.Branch)
	}
	return nil
}
func commaList(s string) []string {
	var rows []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			rows = append(rows, p)
		}
	}
	return unique(rows)
}
func selectPaths(args []string, file string) ([]string, error) {
	rows := append([]string(nil), args...)
	if file != "" {
		b, e := os.ReadFile(file)
		if e != nil {
			return nil, e
		}
		var paths []string
		if e = json.Unmarshal(b, &paths); e != nil {
			return nil, fmt.Errorf("selection file must be a JSON array of paths: %w", e)
		}
		rows = append(rows, paths...)
	}
	var out []string
	for _, p := range rows {
		abs, e := filepath.Abs(p)
		if e != nil {
			return nil, e
		}
		i, e := os.Lstat(abs)
		if e != nil {
			return nil, e
		}
		if !i.IsDir() || i.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("select real checkout directories: %s", abs)
		}
		c, e := canonical(abs)
		if e != nil {
			return nil, e
		}
		out = append(out, c)
	}
	out = unique(out)
	for i, p := range out {
		for j, q := range out {
			if i != j && inside(p, q) {
				return nil, fmt.Errorf("overlapping selection: %s and %s", p, q)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}
