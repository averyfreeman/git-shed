# git-shed

Audit Git checkouts, publish reviewed work, and move recoverable checkouts to system Trash in batches. Go application for macOS and Linux. It defaults to scanning `$HOME/build`.

## Build and install

Requires Go 1.24 or newer and Git 2.23 or newer. GitHub repository creation also requires an authenticated `gh` CLI (`gh auth login`). macOS builds require cgo and Apple's command-line tools to call Foundation's native Trash API; the resulting executable does not require Go at runtime.

```sh
make build
./bin/git-shed version
make install
```

`make install` writes only `~/.local/bin/git-shed`. Add that directory to your PATH if needed. Linux builds do not require cgo. A macOS build made with cgo disabled can audit and publish, but cannot move files to native Trash.

With Go installed, you can also install the latest source release with `go install github.com/averyfreeman/git-shed@latest`.

Tagged releases publish standalone Linux amd64 archives and `.deb`, `.rpm`, and Arch Linux packages, plus macOS Intel and Apple Silicon `.pkg` installers. The macOS packages are currently unsigned and not notarized; macOS may require additional user approval to install them. Building from source with `make install` remains available on both platforms.

## Start with a report

```sh
git-shed scan
git-shed scan --verify --format markdown --output audit.md
git-shed scan --verify --format json --output audit.json
git-shed scan --format csv --output audit.csv
git-shed ui
```

Cached remote-tracking refs are provisional. `--verify` reads the remote's advertised branches and tags, fetches its graph into a temporary bare repository, and checks all local branches and HEAD against it. Existing local objects are shared read-only; missing remote objects are downloaded temporarily and removed afterward. A branch can be synchronized while another local branch contains unpublished commits. Behind-only checkouts can be recoverable even though their current branch differs from the remote.

Reports include origins, push URLs, branch comparisons, unpublished history and tags, staged/modified/untracked/ignored files, stashes, recovery commits in reflogs or stale remote-tracking refs, duplicate origins, nested repositories, errors, blockers, and disk estimates. JSON includes full file and branch lists. Sizes include ignored files and Git metadata. Reports also estimate bytes reclaimable after purge in default leave mode; local-only file trees are excluded from that estimate. The summary counts nested checkout trees once; files shared through hard links, APFS clones, compression, snapshots, or deduplication can make reclaimed space smaller than estimated.

Default discovery exclusions are `node_modules,.venv,venv,target,.next,.cache,vendor`. Excluded paths are listed in the report. Use `--exclude ''` for exhaustive discovery, or supply your own comma-separated directory names. Directory symlinks are not followed. Invalid Git markers and inaccessible paths are reported separately. Remote timeouts or authentication errors never count as successful verification. Shallow repositories receive a `refs_only` check of advertised ref equality and remain blocked for cleanup. Temporary verification uses reftables when supported by Git to handle case-distinct remote refs on macOS. Remote refs that change during verification require a retry.

Export a saved audit without repeating the scan using `git-shed report --input audit.json --format markdown --output audit.md`.

The terminal picker supports filtering (`/`), size/status sorting (`s`), selection (space), selection of filtered rows (`a`), clearing (`x`), and details (`d`). Its action keys are shown on screen. Actions run after leaving the picker and review each repository individually.

## Publish reviewed work

```sh
git-shed push --branch main ./project-a ./project-b
git-shed push --branch feature --tag v1.0 ./project
git-shed commit-push ./project-a ./project-b
git-shed create-origin ./project-without-origin
git-shed open ./project-a ./project-b
git-shed open --owner YOUR_ACCOUNT
```

Pushes use explicit branch/tag refspecs. Missing upstreams are offered the same branch name on origin. Existing upstreams on another remote require explicit Git configuration first. Multiple push URLs are rejected. No automatic force-push, pull, merge, rebase, mirror push, or push of every ref occurs. A separate push URL is displayed; the resulting refs must also be confirmed through origin's fetch URL.

`commit-push` shows changes and diffs, asks for individual staging paths as a JSON array (for example `["src/app.go", "README.md"]`), and asks for a message per checkout. Existing staged changes are retained and displayed; the final staged diff gets another review before commit. Git hooks and signing remain enabled. A hook failure retains staging; a push failure retains successful local commits. Untracked files are selected explicitly rather than staged wholesale.

`create-origin` asks for owner, repository name, and public/private visibility. It uses `gh repo create` and refuses to replace origins or silently attach existing GitHub repositories. It then offers reviewed commits and pushes, including an initial commit for an unborn repository. Noninteractive creation requires `--owner` and `--visibility`; no visibility is assumed.

## Move checkouts to Trash

```sh
git-shed trash ./project-a ./project-b
git-shed trash --files leave ./project
git-shed trash --files backup --backup ~/git-local-files ./project
git-shed trash --files all --discard-reviewed ./project
git-shed entries
```

Every cleanup requires a fresh origin verification and a clean tracked working tree. All branch/HEAD history and local tags must be published. Stashes and in-progress Git operations block removal. Submodules, linked worktrees, checkouts containing nested repositories, shallow repositories, external object stores, custom `core.worktree`, custom refs, legacy grafts, assume-unchanged/skip-worktree files, and Git LFS repositories are reported but protected in this version.

Ignored and untracked files are listed for review. Choose:

- **leave** (default): move tracked files and `.git` to a checkout-shaped payload in Trash. Keep ignored and untracked files at their original paths. The remaining folder is no longer a Git checkout.
- **backup**: move local-only files under a unique entry directory in the chosen backup folder, retaining relative paths. Cross-filesystem regular-file copies are verified before originals are removed. Relative symlinks remain symlinks. File permissions and timestamps are retained; cross-filesystem backups do not promise preservation of extended attributes.
- **all**: move the entire checkout, including local-only data, to Trash. Requires explicit review approval (`--discard-reviewed` for noninteractive usage).

The macOS backend calls Foundation's native Trash API. Linux uses the freedesktop Trash format, including `.trashinfo` records and per-volume Trash when the source is on another filesystem. A Trash failure never falls back to permanent deletion.

**Moving files to Trash does not reclaim disk space.** Purging is a separate reviewed action:

```sh
git-shed entries
git-shed purge ENTRY_ID_1 ENTRY_ID_2
```

Purge re-verifies the origin, refuses changed payloads and recovery commits in reflogs or stale remote-tracking refs, and deletes only the explicitly selected CLI-managed payloads. It never empties unrelated system Trash. To preserve recovery commits, restore the checkout, create branches or tags for the wanted commits, and publish them before trying again.

## Restore or clone again

```sh
git-shed restore ENTRY_ID
git-shed clone --dest ~/build/restored-project ENTRY_ID
```

Restore recovers tracked files and `.git`, plus any backed-up local files, to their recorded paths. Existing files are never overwritten. Clone uses the recorded origin and branch and requires an empty destination; after leave mode, select a fresh directory rather than overwriting the preserved local files. Clone retrieves the mapped remote branch’s current state and restores its local name. A branch without a corresponding remote branch is recreated at its recorded HEAD; a detached checkout returns to its recorded HEAD. If the remote no longer contains that commit, recovery fails clearly while retaining the new clone.

The private journal defaults to `~/.local/share/git-shed`. Override it with `GIT_SHED_STATE` or `--state`. Entries record original paths, Trash paths, branch/HEAD metadata, backup moves, and completion state. Interrupted moves can be recovered with `restore`. If the process stopped after the OS moved a payload but before its destination was recorded, locate `.git-shed-stage-ENTRY_ID` in system Trash and use:

```sh
git-shed restore --payload /actual/Trash/path/.git-shed-stage-ENTRY_ID ENTRY_ID
```

Review entries with `git-shed entries --format json`. Failed entries retain their staging payload and journal details. Do not manually delete a staging directory before recovering it. Trash can also be emptied externally by the operating system; the CLI cannot restore payloads that no longer exist. A purge interrupted after deletion remains recorded as `purging` for manual inspection rather than falsely reporting success.

## Automation

All flags precede positional paths or IDs. A selection file is a JSON array of checkout paths. Use `--format json` for machine-readable operation results (previews and progress go to stderr).

```sh
git-shed push --yes --branch main --selection repos.json --format json
git-shed commit-push --yes --file README.md --message 'Update docs' ./project
git-shed trash --dry-run --selection repos.json
git-shed trash --yes --files leave --selection repos.json
git-shed purge --yes ENTRY_ID
```

`--dry-run` previews without changing checkouts or journals. Trash previews can still verify remote state through temporary storage. A dry-run scan remains entirely local. Independent repositories continue after failures, with per-repository results and a nonzero exit status if any operation fails. Cleanup is never implicitly chained after a push failure. Run `scan --verify` again before selecting eligible repositories for Trash. Work on selected checkouts should stop during review and movement; Git and filesystem state are checked again immediately before moving files.

## Validation

```sh
make test
make check
```

Tests use disposable checkouts and local bare remotes. They cover all-branch publication, ahead/behind/diverged histories, deleted remote refs, tags, detached and unborn HEAD, ignored files, stashes, worktrees, LFS, hooks, failed pushes, explicit staging, GitHub/browser mocks, path protection, Trash, restore, purge isolation, changed payloads, and interrupted moves. Native Trash tests use small temporary payloads and clean up their entries. No existing user project is committed, pushed, or removed by the test suite.
