package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

// version is set by GoReleaser at build time.
var version = "dev"

type multiFlag []string

func (f *multiFlag) String() string     { return strings.Join(*f, ",") }
func (f *multiFlag) Set(s string) error { *f = append(*f, s); return nil }
func usage(w io.Writer) {
	fmt.Fprintln(w, `git-shed — audit and retire Git checkouts

Usage: git-shed COMMAND [FLAGS] [PATHS or ENTRY IDS]
Commands: scan, report, ui, push, commit-push, create-origin, open, trash,
          entries, restore, purge, clone, version

Examples:
  git-shed scan --verify --format markdown --output audit.md
  git-shed ui
  git-shed push --branch main ./project-a ./project-b
  git-shed trash --files leave ./project
  git-shed entries
  git-shed purge ENTRY_ID

Flags precede positional paths. Every mutation previews and asks for confirmation;
--yes confirms explicitly supplied options. --dry-run never mutates repositories.
Run git-shed COMMAND --help for available flags.
Trash does not reclaim space until its contents are purged.`)
}
func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if e := run(ctx, os.Args[1:], os.Stdout, os.Stderr, os.Stdin); e != nil {
		fmt.Fprintln(os.Stderr, "git-shed:", redactText(e.Error()))
		os.Exit(1)
	}
}
func run(ctx context.Context, args []string, out, errout io.Writer, input io.Reader) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		usage(out)
		return nil
	}
	cmd := args[0]
	if cmd == "version" {
		fmt.Fprintln(out, version)
		return nil
	}
	f := flag.NewFlagSet(cmd, flag.ContinueOnError)
	f.SetOutput(errout)
	root := f.String("root", ".", "scan root")
	workers := f.Int("workers", 4, "parallel audit workers (1–32)")
	verify := f.Bool("verify", false, "freshly verify origin refs and graphs")
	sizes := f.Bool("sizes", true, "measure approximate logical and allocated disk usage")
	exclude := f.String("exclude", "node_modules,.venv,venv,target,.next,.cache,vendor", "comma-separated directory names to exclude; empty scans everything")
	format := f.String("format", "table", "table, json, csv, markdown; actions support table or json")
	output := f.String("output", "", "report output file (default stdout)")
	inputReport := f.String("input", "", "saved audit JSON to export (report command)")
	selection := f.String("selection", "", "JSON array of checkout paths")
	yes := f.Bool("yes", false, "confirm explicitly supplied mutation options")
	dry := f.Bool("dry-run", false, "preview actions without repository or journal mutations")
	mode := f.String("files", "leave", "local file handling: leave, backup, all")
	backup := f.String("backup", "", "backup destination for --files backup")
	discard := f.Bool("discard-reviewed", false, "confirm reviewed ignored/untracked files may enter Trash in all mode")
	owner := f.String("owner", "", "GitHub owner (required for noninteractive creation)")
	visibility := f.String("visibility", "", "GitHub visibility: private or public")
	message := f.String("message", "", "commit message; interactive default asks per repository")
	dest := f.String("dest", "", "empty clone destination")
	payload := f.String("payload", "", "recover a payload whose Trash destination was not journaled (restore only)")
	state := f.String("state", "", "journal directory (default GIT_SHED_STATE or ~/.local/share/git-shed)")
	var branches, tags, files multiFlag
	f.Var(&branches, "branch", "branch to push (repeatable)")
	f.Var(&tags, "tag", "tag to push (repeatable)")
	f.Var(&files, "file", "individual file to stage (repeatable)")
	if e := f.Parse(args[1:]); errors.Is(e, flag.ErrHelp) {
		return nil
	} else if e != nil {
		return e
	}
	a := Actions{In: bufio.NewReader(input), Out: errout, Yes: *yes, DryRun: *dry, Branches: branches, Tags: tags, Files: files, Message: *message, Mode: *mode, Backup: *backup, Discard: *discard, Owner: *owner, Visibility: *visibility}
	store, e := defaultStore()
	if e != nil {
		return e
	}
	if *state != "" {
		store.Dir, e = canonicalDestination(*state)
		if e != nil {
			return e
		}
	}
	if cmd == "report" {
		if *inputReport == "" {
			return fmt.Errorf("report requires --input")
		}
		b, e := os.ReadFile(*inputReport)
		if e != nil {
			return e
		}
		var r Report
		if e = json.Unmarshal(b, &r); e != nil {
			return e
		}
		if r.Version != 1 {
			return fmt.Errorf("unsupported report version")
		}
		w := out
		if *output != "" {
			file, e := os.OpenFile(*output, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
			if e != nil {
				return e
			}
			defer file.Close()
			w = file
		}
		return writeReport(w, r, *format)
	}
	if cmd == "scan" || cmd == "ui" {
		if len(f.Args()) > 0 {
			if len(f.Args()) != 1 {
				return fmt.Errorf("scan/ui accepts one root")
			}
			*root = f.Args()[0]
		}
		if *dry {
			*verify = false
		} // Dry-run scanning is entirely local.
		fmt.Fprintf(errout, "Scanning %s (verification=%v)…\n", *root, *verify)
		r, e := Scan(ctx, ScanOptions{Root: *root, Workers: *workers, Verify: *verify, Sizes: *sizes, Excludes: commaList(*exclude), Progress: func(done, total int, r Repo) {
			if done%25 == 0 || done == total || r.Verification == "failed" {
				fmt.Fprintf(errout, "%d/%d · %s · %s\n", done, total, r.Verification, r.Path)
			}
		}})
		if e != nil {
			return e
		}
		if cmd == "scan" {
			writer := out
			var file *os.File
			if *output != "" {
				file, e = os.OpenFile(*output, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
				if e != nil {
					return e
				}
				defer file.Close()
				writer = file
			}
			return writeReport(writer, r, *format)
		}
		action, paths, e := pick(r.Repositories)
		if e != nil || action == "" {
			return e
		}
		cmd = action
		args = paths
	} else {
		args = f.Args()
	}
	if cmd == "entries" {
		rows, e := store.list()
		if e != nil {
			return e
		}
		if *format == "json" {
			return json.NewEncoder(out).Encode(rows)
		}
		for _, r := range rows {
			fmt.Fprintf(out, "%s  %-10s %9s  %s\n", r.ID, r.State, humanSize(r.Bytes), r.Original)
		}
		return nil
	}
	if (cmd == "trash" || cmd == "restore" || cmd == "purge") && !a.DryRun {
		unlock, e := store.lock()
		if e != nil {
			return e
		}
		defer unlock()
	}
	var results []Result
	if cmd == "restore" || cmd == "purge" || cmd == "clone" {
		if len(args) == 0 {
			return fmt.Errorf("select one or more journal entry IDs with %s", cmd)
		}
		if cmd == "clone" && len(args) > 1 {
			return fmt.Errorf("clone accepts one entry")
		}
		if *payload != "" && len(args) != 1 {
			return fmt.Errorf("--payload requires exactly one entry")
		}
		for _, id := range unique(args) {
			r := Result{Target: id, Action: cmd, Status: "failed"}
			entry, e := store.load(id)
			if e == nil {
				fmt.Fprintf(errout, "%s %s (%s), approximately %s\n", cmd, id, entry.Original, humanSize(entry.Bytes))
				switch cmd {
				case "clone":
					e = Clone(ctx, entry, *dest, a)
				case "restore", "purge":
					e = a.confirm(cmd + " this entry?")
					if e == nil && !a.DryRun {
						if cmd == "restore" {
							e = Restore(ctx, store, &entry, *payload)
						} else {
							e = Purge(ctx, store, &entry)
						}
					}
				}
			}
			if e == nil {
				r.Status = "success"
				if a.DryRun {
					r.Status = "preview"
				}
			} else {
				r.Detail = redactText(e.Error())
			}
			results = append(results, r)
		}
	} else {
		if cmd != "push" && cmd != "commit-push" && cmd != "create-origin" && cmd != "open" && cmd != "trash" {
			return fmt.Errorf("unknown command %q", cmd)
		}
		if cmd == "open" && *owner != "" {
			if !validGitHubName(*owner) {
				return fmt.Errorf("invalid owner")
			}
			url := "https://github.com/" + *owner + "?tab=repositories"
			fmt.Fprintln(errout, url)
			if !a.DryRun {
				return openURL(ctx, url)
			}
			return nil
		}
		paths, e := selectPaths(args, *selection)
		if e != nil {
			return e
		}
		if len(paths) == 0 {
			return fmt.Errorf("select repository paths or use --selection")
		}
		for _, path := range paths {
			r := Result{Target: path, Action: cmd, Status: "failed"}
			repo := Inspect(ctx, path, cmd == "trash", cmd == "trash" || cmd == "push")
			var e error
			if repo.Kind == "invalid" {
				e = fmt.Errorf("invalid Git checkout: %s", path)
			} else {
				switch cmd {
				case "push":
					e = Push(ctx, repo, a)
				case "commit-push":
					e = CommitPush(ctx, repo, a)
				case "create-origin":
					e = CreateOrigin(ctx, repo, a)
				case "open":
					var u string
					u, e = browserURL(repo.Origin)
					if e == nil {
						fmt.Fprintln(errout, u)
						if !a.DryRun {
							e = openURL(ctx, u)
						}
					}
				case "trash":
					local := a
					repo.FileSnapshot, e = metadataDigest(ctx, repo.Path)
					if e != nil {
						break
					}
					fmt.Fprintf(errout, "\n%s\nOrigin: %s\nVerification: %s; local-only commits: %d\nBlockers: %s\nApproximate allocated size: %s\n", repo.Path, redact(repo.Origin), repo.Verification, repo.LocalOnly, strings.Join(repo.Blockers, "; "), humanSize(repo.AllocatedBytes))
					for _, group := range []struct {
						name string
						rows []string
					}{{"Ignored", repo.Ignored}, {"Untracked", repo.UntrackedRoots}} {
						fmt.Fprintf(errout, "%s (%d):\n", group.name, len(group.rows))
						for _, s := range group.rows {
							bytes, known := repo.IgnoredSizes[s]
							if group.name == "Untracked" {
								bytes, known = repo.UntrackedSizes[s]
							}
							if known {
								fmt.Fprintf(errout, "  %s (%s allocated)\n", s, humanSize(bytes))
							} else {
								fmt.Fprintln(errout, "  "+s)
							}
						}
					}
					if len(repo.Blockers) > 0 {
						e = fmt.Errorf("not eligible: %s", strings.Join(repo.Blockers, "; "))
						break
					}
					if !a.Yes && !a.DryRun && (len(repo.Ignored) > 0 || len(repo.Untracked) > 0) {
						var s string
						s, e = a.ask("Local files: leave in place / backup / all to Trash (empty = leave):")
						if e != nil {
							break
						}
						if s != "" {
							local.Mode = s
						}
						if local.Mode == "backup" && local.Backup == "" {
							local.Backup, e = a.ask("Backup destination:")
							if e != nil {
								break
							}
						}
						if local.Mode == "all" {
							e = a.confirm("Include all listed ignored/untracked files in Trash?")
							if e != nil {
								break
							}
							local.Discard = true
						}
					}
					if local.Mode != "leave" && local.Mode != "backup" && local.Mode != "all" {
						e = fmt.Errorf("file mode must be leave, backup, or all")
						break
					}
					if local.Mode == "backup" && local.Backup == "" {
						e = fmt.Errorf("backup mode requires --backup")
						break
					}
					if local.Mode == "all" && (len(repo.Ignored) > 0 || len(repo.Untracked) > 0) && !local.Discard {
						e = fmt.Errorf("all mode requires --discard-reviewed for listed local files")
						break
					}
					estimated, known := estimateLeave(repo)
					if known {
						fmt.Fprintf(errout, "Estimated reclaimable after purge with leave mode: %s.\n", humanSize(estimated))
					}
					fmt.Fprintf(errout, "File handling: %s; backup: %s. Trash bytes are reclaimed only after purge.\n", local.Mode, local.Backup)
					e = local.confirm("Move this checkout to system Trash?")
					if e == nil && !local.DryRun {
						var entry Entry
						entry, e = TrashRepo(ctx, store, repo, local.Mode, local.Backup, local.Discard)
						r.EntryID = entry.ID
						if entry.State == "trashed" {
							fmt.Fprintf(errout, "Trashed %s (%s). Retained until purge.\n", entry.ID, humanSize(entry.Bytes))
						}
					}
				}
			}
			if e == nil {
				r.Status = "success"
				if a.DryRun {
					r.Status = "preview"
				}
			} else {
				r.Detail = redactText(e.Error())
			}
			results = append(results, r)
			if ctx.Err() != nil {
				break
			}
		}
	}
	failed := false
	for _, r := range results {
		if r.Status == "failed" {
			failed = true
		}
	}
	if *format == "json" {
		e = json.NewEncoder(out).Encode(results)
	} else {
		for _, r := range results {
			fmt.Fprintf(out, "%s: %s %s %s\n", r.Status, r.Action, r.Target, r.EntryID)
			if r.Detail != "" {
				fmt.Fprintln(out, "  "+r.Detail)
			}
		}
	}
	if e != nil {
		return e
	}
	if failed {
		return fmt.Errorf("one or more operations failed; see per-repository results")
	}
	return nil
}
