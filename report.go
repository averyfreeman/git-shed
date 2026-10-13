package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
)

func jsonMarshal(v any) ([]byte, error)   { return json.Marshal(v) }
func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

var urlCredentials = regexp.MustCompile(`(https?://|ssh://)[^\s/@]+(?::[^\s/@]*)?@`)

func redactText(s string) string { return urlCredentials.ReplaceAllString(s, "${1}[redacted]@") }
func humanSize(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	v := float64(n)
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	for _, u := range units {
		v /= 1024
		if v < 1024 || u == "TiB" {
			return fmt.Sprintf("%.1f %s", v, u)
		}
	}
	return ""
}
func writeReport(w io.Writer, r Report, format string) error {
	r = publicReport(r)
	switch format {
	case "json":
		e := json.NewEncoder(w)
		e.SetIndent("", "  ")
		return e.Encode(r)
	case "csv":
		c := csv.NewWriter(w)
		_ = c.Write([]string{"path", "origin", "status", "verification", "verified_at", "local_only_commits", "tracked_changes", "untracked_files", "ignored_entries", "stashes", "bytes", "allocated_bytes", "estimated_leave_bytes", "blockers"})
		for _, p := range r.Repositories {
			_ = c.Write([]string{p.Path, p.Origin, p.Status, p.Verification, p.VerifiedAt, strconv.Itoa(p.LocalOnly), strconv.Itoa(len(p.Changes)), strconv.Itoa(len(p.Untracked)), strconv.Itoa(len(p.Ignored)), strconv.Itoa(p.Stashes), strconv.FormatInt(p.Bytes, 10), strconv.FormatInt(p.AllocatedBytes, 10), estimateCSV(p), strings.Join(p.Blockers, "; ")})
		}
		c.Flush()
		return c.Error()
	case "markdown":
		fmt.Fprintf(w, "# Git checkout audit\n\nRoot: `%s`  \nScanned: %s\n\nCounts overlap. Cached comparisons do not confirm current server state. Disk estimates include checkout contents; nested trees are counted once in the summary.\n\n", r.Root, r.ScannedAt)
		keys := make([]string, 0, len(r.Summary))
		for k := range r.Summary {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(w, "- %s: %d\n", k, r.Summary[k])
		}
		fmt.Fprintln(w, "\n| Path | Origin | Current branch | Verification | Local-only commits | Changes / untracked / ignored | Size / leave estimate | Blockers |\n|---|---|---|---|---:|---|---:|---|")
		esc := func(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, "|", "\\|"), "\n", " ") }
		for _, p := range r.Repositories {
			fmt.Fprintf(w, "| %s | %s | %s | %s | %d | %d / %d / %d | %s | %s |\n", esc(p.Path), esc(p.Origin), esc(p.Status), p.Verification, p.LocalOnly, len(p.Changes), len(p.Untracked), len(p.Ignored), humanSize(p.AllocatedBytes)+" / "+leaveLabel(p), esc(strings.Join(p.Blockers, "; ")))
		}
		fmt.Fprint(w, "\n## Categories\n\n")
		groups := map[string][]string{}
		for _, p := range r.Repositories {
			groups[p.Status] = append(groups[p.Status], p.Path)
			if p.LocalOnly > 0 {
				groups["commits absent from origin refs"] = append(groups["commits absent from origin refs"], p.Path)
			}
		}
		keys = nil
		for k := range groups {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(w, "### %s\n\n", k)
			for _, p := range groups[k] {
				fmt.Fprintf(w, "- `%s`\n", p)
			}
			fmt.Fprintln(w)
		}
		fmt.Fprint(w, "## Branches requiring attention\n\n")
		for _, p := range r.Repositories {
			attention := p.LocalOnly > 0 || p.Status == "detached"
			for _, b := range p.Branches {
				attention = attention || b.Ahead > 0 || b.Status == "missing_upstream" || b.Status == "missing_remote_branch"
			}
			if !attention {
				continue
			}
			fmt.Fprintf(w, "### %s\n\nHEAD: `%s`\n\n", p.Path, p.Head)
			fmt.Fprintln(w, "| Branch | Upstream | Ahead | Behind | Status |\n|---|---|---:|---:|---|")
			for _, b := range p.Branches {
				fmt.Fprintf(w, "| %s | %s | %d | %d | %s |\n", esc(b.Name), esc(b.Upstream), b.Ahead, b.Behind, esc(b.Status))
			}
			fmt.Fprintln(w)
		}
		fmt.Fprint(w, "## Duplicate origins\n\n")
		keys = nil
		for k := range r.Duplicates {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(w, "### %s\n\n", k)
			for _, p := range r.Duplicates[k] {
				fmt.Fprintf(w, "- `%s`\n", p)
			}
		}
		for _, group := range []struct {
			name string
			rows []string
		}{{"Invalid markers", r.Invalid}, {"Excluded directories", r.Excluded}, {"Inspection errors", r.Errors}} {
			if len(group.rows) > 0 {
				fmt.Fprintf(w, "\n## %s\n\n", group.name)
				for _, s := range group.rows {
					fmt.Fprintf(w, "- %s\n", s)
				}
			}
		}
		return nil
	case "table":
		t := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		fmt.Fprintln(t, "PATH\tSTATUS\tVERIFY\tLOCAL ONLY\tDIRTY/U/I\tALLOCATED/LEAVE\tORIGIN")
		for _, p := range r.Repositories {
			fmt.Fprintf(t, "%s\t%s\t%s\t%d\t%d/%d/%d\t%s\t%s\n", p.Path, p.Status, p.Verification, p.LocalOnly, len(p.Changes), len(p.Untracked), len(p.Ignored), humanSize(p.AllocatedBytes)+"/"+leaveLabel(p), p.Origin)
		}
		if e := t.Flush(); e != nil {
			return e
		}
		fmt.Fprintf(w, "\n%d repositories; %d invalid markers; %d excluded directories; %d inspection errors.\n", len(r.Repositories), len(r.Invalid), len(r.Excluded), len(r.Errors))
		fmt.Fprintln(w, "Cached comparisons are provisional. Sizes are approximate; Trash does not free space until purged.")
		return nil
	default:
		return fmt.Errorf("unknown format %q", format)
	}
}
func estimateLeave(r Repo) (int64, bool) {
	if !r.SizeKnown {
		return 0, false
	}
	type entry struct {
		path  string
		bytes int64
	}
	var entries []entry
	for _, group := range []struct {
		paths []string
		sizes map[string]int64
	}{{r.Ignored, r.IgnoredSizes}, {r.UntrackedRoots, r.UntrackedSizes}} {
		for _, p := range group.paths {
			size, ok := group.sizes[p]
			if !ok {
				return 0, false
			}
			entries = append(entries, entry{strings.TrimSuffix(p, "/"), size})
		}
	}
	if len(r.Untracked) > 0 && len(r.UntrackedRoots) == 0 {
		return 0, false
	}
	total := r.AllocatedBytes
	for i, p := range entries {
		covered := false
		for j, q := range entries {
			if i != j && (p.path == q.path && j < i || p.path != q.path && strings.HasPrefix(p.path, q.path+"/")) {
				covered = true
				break
			}
		}
		if !covered {
			total -= p.bytes
		}
	}
	if total < 0 {
		total = 0
	}
	return total, true
}
func leaveLabel(r Repo) string {
	if !r.LeaveEstimateKnown {
		return "unknown"
	}
	return humanSize(r.EstimatedLeaveBytes)
}
func estimateCSV(r Repo) string {
	if !r.LeaveEstimateKnown {
		return ""
	}
	return strconv.FormatInt(r.EstimatedLeaveBytes, 10)
}
