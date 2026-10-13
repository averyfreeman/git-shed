package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

func advertised(ctx context.Context, origin string) (map[string]string, error) {
	s, e := command(ctx, "", "git", "ls-remote", "--refs", origin, "refs/heads/*", "refs/tags/*")
	if e != nil {
		return nil, e
	}
	m := map[string]string{}
	for _, line := range lines(s) {
		f := strings.Fields(line)
		if len(f) != 2 {
			return nil, fmt.Errorf("malformed remote advertisement")
		}
		m[f[1]] = f[0]
	}
	return m, nil
}
func refsKey(m map[string]string) string {
	a := make([]string, 0, len(m))
	for k, v := range m {
		a = append(a, k+"="+v)
	}
	sort.Strings(a)
	return strings.Join(a, "\n")
}
func verifyRemote(ctx context.Context, r *Repo) {
	fail := func(e error) {
		r.Verification = "failed"
		r.Status = "inaccessible_or_indeterminate"
		r.Errors = append(r.Errors, "origin verification: "+e.Error())
		r.Blockers = append(r.Blockers, "origin could not be verified")
	}
	before, e := advertised(ctx, r.Origin)
	if e != nil {
		fail(e)
		return
	}
	if containsBlocker(r.Blockers, "shallow repository") {
		verifyShallowRefs(ctx, r, before)
		return
	}
	tmp, e := os.MkdirTemp("", "git-shed-verify-")
	if e != nil {
		fail(e)
		return
	}
	defer os.RemoveAll(tmp)
	// Reftables handle remote names differing only by case on macOS volumes.
	if _, e = command(ctx, "", "git", "init", "--bare", "--quiet", "--ref-format=reftable", tmp); e != nil {
		if _, e = command(ctx, "", "git", "init", "--bare", "--quiet", tmp); e != nil {
			fail(e)
			return
		}
	}
	// Reuse existing objects read-only so auditing large repositories does not
	// duplicate their history on the disk we are trying to clean.
	objects := strings.TrimSpace(optionalGit(ctx, r.Path, "rev-parse", "--git-path", "objects"))
	if !filepath.IsAbs(objects) {
		objects = filepath.Join(r.Path, objects)
	}
	if strings.ContainsAny(objects, "\r\n") {
		fail(fmt.Errorf("object-store path contains a newline"))
		return
	}
	if e = os.MkdirAll(filepath.Join(tmp, "objects", "info"), 0700); e != nil {
		fail(e)
		return
	}
	if e = os.WriteFile(filepath.Join(tmp, "objects", "info", "alternates"), []byte(objects+"\n"), 0600); e != nil {
		fail(e)
		return
	}
	if _, e = git(ctx, tmp, "config", "core.hooksPath", os.DevNull); e != nil {
		fail(e)
		return
	}
	if len(before) > 0 {
		_, e = git(ctx, tmp, "-c", "gc.auto=0", "-c", "maintenance.auto=false", "fetch", "--quiet", "--no-tags", "--filter=blob:none", r.Origin, "+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*")
		if e != nil {
			fail(e)
			return
		}
	}
	fetched := map[string]string{}
	s, e := git(ctx, tmp, "for-each-ref", "--format=%(refname)%09%(objectname)", "refs/heads", "refs/tags")
	if e != nil {
		fail(e)
		return
	}
	for _, line := range lines(s) {
		p := strings.SplitN(line, "\t", 2)
		if len(p) == 2 {
			fetched[p[0]] = p[1]
		}
	}
	after, e := advertised(ctx, r.Origin)
	if e != nil {
		fail(e)
		return
	}
	if refsKey(before) != refsKey(after) || refsKey(before) != refsKey(fetched) {
		fail(fmt.Errorf("origin changed during verification; retry"))
		return
	}

	for i := range r.Branches {
		b := &r.Branches[i]
		ref := "refs/heads/" + b.Name
		if strings.HasPrefix(b.Upstream, "origin/") {
			ref = "refs/heads/" + strings.TrimPrefix(b.Upstream, "origin/")
		}
		if _, ok := before[ref]; ok {
			setBranchCounts(ctx, tmp, b, b.OID, ref)
			if b.Upstream == "" {
				b.Status = "missing_upstream (" + b.Status + ")"
			}
		} else {
			b.Ahead = -1
			b.Behind = -1
			b.Status = "missing_remote_branch"
		}
	}
	r.UnpublishedTags = nil
	for name, oid := range r.Tags {
		if before["refs/tags/"+name] != oid {
			r.UnpublishedTags = append(r.UnpublishedTags, name)
		}
	}
	sort.Strings(r.UnpublishedTags)
	if r.Head != "" {
		args := []string{"rev-list", "--count", r.Head}
		for _, b := range r.Branches {
			args = append(args, b.OID)
		}
		args = append(args, "--not")
		for ref := range before {
			args = append(args, ref)
		}
		s, e = git(ctx, tmp, args...)
		if e != nil {
			fail(e)
			return
		}
		r.LocalOnly, e = strconv.Atoi(strings.TrimSpace(s))
		if e != nil {
			fail(e)
			return
		}
	}
	if r.Head != "" {
		// Preserve reflog-only and stale remote-tracking history before permanent purge.
		localTips, e := git(ctx, r.Path, "rev-list", "--no-walk", "--all", "--reflog", "HEAD")
		if e != nil {
			fail(e)
			return
		}
		var updates strings.Builder
		for i, oid := range unique(lines(localTips)) {
			fmt.Fprintf(&updates, "update refs/recovery/%d %s\n", i, oid)
		}
		if updates.Len() > 0 {
			if _, e = commandInput(ctx, "", "git", updates.String(), "-C", tmp, "update-ref", "--stdin"); e != nil {
				fail(e)
				return
			}
		}
		recoveryArgs := []string{"rev-list", "--count", "--all", "--not", r.Head}
		for _, b := range r.Branches {
			recoveryArgs = append(recoveryArgs, b.OID)
		}
		for _, oid := range r.Tags {
			recoveryArgs = append(recoveryArgs, oid)
		}
		for ref := range before {
			recoveryArgs = append(recoveryArgs, ref)
		}
		recovery, e := git(ctx, tmp, recoveryArgs...)
		if e != nil {
			fail(e)
			return
		}
		r.RecoveryCommits, e = strconv.Atoi(strings.TrimSpace(recovery))
		if e != nil {
			fail(e)
			return
		}
	}
	r.Verification = "verified"
	r.VerifiedAt = time.Now().UTC().Format(time.RFC3339)
	r.Status = currentStatus(*r)
	if r.LocalOnly > 0 {
		r.Blockers = append(r.Blockers, fmt.Sprintf("%d commits absent from origin", r.LocalOnly))
	}
	if len(r.UnpublishedTags) > 0 {
		r.Blockers = append(r.Blockers, "unpublished or differing tags")
	}
}
func redact(s string) string {
	u, e := url.Parse(s)
	if e == nil && u.Scheme != "" && u.Host != "" {
		u.User = nil
		u.RawQuery = ""
		u.Fragment = ""
		return u.String()
	}
	return s
}
func publicReport(r Report) Report { // Deep copy so redaction never changes action targets.
	b, _ := jsonMarshal(r)
	var out Report
	_ = jsonUnmarshal(b, &out)
	for i := range out.Repositories {
		p := &out.Repositories[i]
		p.EstimatedLeaveBytes, p.LeaveEstimateKnown = estimateLeave(*p)
		if len(p.Tracked) > 0 {
			p.TrackedFiles = len(p.Tracked)
		}
		p.Tracked = nil
		old := p.Origin
		p.Origin = redact(p.Origin)
		for j := range p.PushURLs {
			p.PushURLs[j] = redact(p.PushURLs[j])
		}
		for j := range p.Errors {
			p.Errors[j] = redactText(strings.ReplaceAll(p.Errors[j], old, p.Origin))
		}
	}
	for i := range out.Errors {
		out.Errors[i] = redactText(out.Errors[i])
	}
	dups := map[string][]string{}
	for _, p := range out.Repositories {
		if p.Origin != "" {
			k := normalizeOrigin(p.Origin)
			dups[k] = append(dups[k], p.Path)
		}
	}
	for k, v := range dups {
		if len(v) < 2 {
			delete(dups, k)
		}
	}
	out.Duplicates = dups
	if out.Summary == nil {
		out.Summary = map[string]int64{}
	}
	out.Summary["estimated_eligible_leave_reclaimable_bytes"] = 0
	for _, p := range out.Repositories {
		if len(p.Blockers) == 0 && p.LeaveEstimateKnown {
			out.Summary["estimated_eligible_leave_reclaimable_bytes"] += p.EstimatedLeaveBytes
		}
	}
	return out
}
func containsBlocker(blockers []string, s string) bool {
	for _, b := range blockers {
		if strings.Contains(b, s) {
			return true
		}
	}
	return false
}
func verifyShallowRefs(ctx context.Context, r *Repo, refs map[string]string) {
	after, e := advertised(ctx, r.Origin)
	if e != nil || refsKey(refs) != refsKey(after) {
		r.Verification = "failed"
		r.Status = "inaccessible_or_indeterminate"
		r.Blockers = append(r.Blockers, "origin refs could not be verified")
		if e != nil {
			r.Errors = append(r.Errors, e.Error())
		}
		return
	}
	r.Verification = "refs_only"
	r.VerifiedAt = time.Now().UTC().Format(time.RFC3339)
	r.LocalOnly = -1
	r.Blockers = append(r.Blockers, "shallow graph: only advertised ref equality was verified")
	tips := map[string]bool{}
	for _, oid := range refs {
		tips[oid] = true
	}
	all := tips[r.Head]
	for i := range r.Branches {
		b := &r.Branches[i]
		ref := "refs/heads/" + b.Name
		if strings.HasPrefix(b.Upstream, "origin/") {
			ref = "refs/heads/" + strings.TrimPrefix(b.Upstream, "origin/")
		}
		b.Ahead = -1
		b.Behind = -1
		b.Status = "unknown (shallow)"
		if remote, ok := refs[ref]; !ok {
			b.Status = "missing_remote_branch"
		} else if remote == b.OID {
			b.Ahead = 0
			b.Behind = 0
			b.Status = "synchronized"
			if b.Upstream == "" {
				b.Status = "missing_upstream (synchronized)"
			}
		}
		all = all && tips[b.OID]
	}
	if all {
		r.LocalOnly = 0
	}
	for name, oid := range r.Tags {
		if refs["refs/tags/"+name] != oid {
			r.UnpublishedTags = append(r.UnpublishedTags, name)
		}
	}
	sort.Strings(r.UnpublishedTags)
	if len(r.UnpublishedTags) > 0 {
		r.Blockers = append(r.Blockers, "unpublished or differing tags")
	}
	r.Status = currentStatus(*r)
}
