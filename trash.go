package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Move struct {
	From string `json:"from"`
	To   string `json:"to"`
}
type Entry struct {
	ID          string            `json:"id"`
	CreatedAt   string            `json:"created_at"`
	State       string            `json:"state"`
	Original    string            `json:"original"`
	Origin      string            `json:"origin"`
	Branch      string            `json:"branch"`
	Head        string            `json:"head"`
	Branches    []Branch          `json:"branches"`
	Tags        map[string]string `json:"tags"`
	Mode        string            `json:"file_mode"`
	Backup      string            `json:"backup,omitempty"`
	Stage       string            `json:"stage"`
	Trash       string            `json:"trash,omitempty"`
	Info        string            `json:"trash_info,omitempty"`
	Moves       []Move            `json:"moves"`
	BackupMoves []Move            `json:"backup_moves,omitempty"`
	Digest      string            `json:"payload_digest,omitempty"`
	Bytes       int64             `json:"allocated_bytes"`
	Error       string            `json:"error,omitempty"`
}
type Store struct{ Dir string }

func defaultStore() (Store, error) {
	base := os.Getenv("GIT_SHED_STATE")
	if base == "" {
		home, e := os.UserHomeDir()
		if e != nil {
			return Store{}, e
		}
		base = filepath.Join(home, ".local", "share", "git-shed")
	}
	p, e := canonicalDestination(base)
	return Store{p}, e
}
func (s Store) init() error {
	if e := os.MkdirAll(s.Dir, 0700); e != nil {
		return e
	}
	i, e := os.Lstat(s.Dir)
	if e != nil {
		return e
	}
	if !i.IsDir() || i.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("unsafe journal directory")
	}
	return os.Chmod(s.Dir, 0700)
}
func (s Store) save(e *Entry) error {
	if err := s.init(); err != nil {
		return err
	}
	if !validID(e.ID) {
		return fmt.Errorf("invalid entry ID")
	}
	b, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.Dir, ".journal-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(name, filepath.Join(s.Dir, e.ID+".json")); err != nil {
		return err
	}
	d, err := os.Open(s.Dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func validID(s string) bool {
	if len(s) != 24 {
		return false
	}
	_, e := hex.DecodeString(s)
	return e == nil
}
func (s Store) load(id string) (Entry, error) {
	if !validID(id) {
		return Entry{}, fmt.Errorf("invalid journal ID")
	}
	b, e := os.ReadFile(filepath.Join(s.Dir, id+".json"))
	if e != nil {
		return Entry{}, e
	}
	var out Entry
	e = json.Unmarshal(b, &out)
	if out.ID != id {
		return out, fmt.Errorf("journal ID mismatch")
	}
	return out, e
}
func (s Store) list() ([]Entry, error) {
	ds, e := os.ReadDir(s.Dir)
	if os.IsNotExist(e) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	var rows []Entry
	for _, d := range ds {
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".json") {
			continue
		}
		r, e := s.load(strings.TrimSuffix(d.Name(), ".json"))
		if e != nil {
			return nil, e
		}
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].CreatedAt < rows[j].CreatedAt })
	return rows, nil
}
func newID() string {
	b := make([]byte, 12)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func exists(p string) bool { _, e := os.Lstat(p); return e == nil }
func safePath(root, rel string) (string, error) {
	if rel == "" || filepath.IsAbs(rel) || rel == "." {
		return "", fmt.Errorf("invalid relative file path %q", rel)
	}
	p := filepath.Join(root, rel)
	if !inside(root, p) || p == root {
		return "", fmt.Errorf("path escapes checkout")
	}
	for q := filepath.Dir(p); q != root; q = filepath.Dir(q) {
		i, e := os.Lstat(q)
		if e != nil && !os.IsNotExist(e) {
			return "", e
		}
		if e == nil && (i.Mode()&os.ModeSymlink != 0 || !i.IsDir()) {
			return "", fmt.Errorf("unsafe ancestor: %s", q)
		}
	}
	return p, nil
}
func renameNoClobber(from, to string) error {
	if exists(to) {
		return fmt.Errorf("destination exists: %s", to)
	}
	if e := safeAncestors(to); e != nil {
		return e
	}
	if e := os.MkdirAll(filepath.Dir(to), 0700); e != nil {
		return e
	}
	return os.Rename(from, to)
}
func safeAncestors(p string) error {
	for q := filepath.Dir(p); ; q = filepath.Dir(q) {
		i, e := os.Lstat(q)
		if e != nil && !os.IsNotExist(e) {
			return e
		}
		if e == nil && (!i.IsDir() || i.Mode()&os.ModeSymlink != 0) {
			return fmt.Errorf("unsafe destination ancestor: %s", q)
		}
		if q == filepath.Dir(q) {
			break
		}
	}
	return nil
}
func treeDigest(ctx context.Context, root string) (string, error) {
	h := sha256.New()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, e error) error {
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
		rel, e := filepath.Rel(root, p)
		if e != nil {
			return e
		}
		fmt.Fprintf(h, "%q %v\x00", rel, i.Mode())
		if i.Mode()&os.ModeSymlink != 0 {
			s, e := os.Readlink(p)
			if e != nil {
				return e
			}
			fmt.Fprintf(h, "%q\x00", s)
		} else if i.Mode().IsRegular() {
			f, e := os.Open(p)
			if e != nil {
				return e
			}
			_, e = io.Copy(h, f)
			ce := f.Close()
			if e != nil {
				return e
			}
			if ce != nil {
				return ce
			}
		} else if !i.IsDir() {
			return fmt.Errorf("unsupported special file: %s", p)
		}
		fmt.Fprint(h, "\x00")
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func TrashRepo(ctx context.Context, store Store, r Repo, mode, backup string, discard bool) (Entry, error) {
	e := Entry{ID: newID(), CreatedAt: time.Now().UTC().Format(time.RFC3339), State: "prepared", Original: r.Path, Origin: redact(r.Origin), Branch: r.Branch, Head: r.Head, Branches: r.Branches, Tags: r.Tags, Mode: mode, Bytes: r.AllocatedBytes}
	fail := func(err error) (Entry, error) { e.Error = redactText(err.Error()); _ = store.save(&e); return e, err }
	if len(r.Blockers) > 0 || r.Verification != "verified" {
		return e, fmt.Errorf("not eligible: %s", strings.Join(r.Blockers, "; "))
	}
	if mode != "leave" && mode != "backup" && mode != "all" {
		return e, fmt.Errorf("file mode must be leave, backup, or all")
	}
	if mode == "all" && (len(r.Ignored) > 0 || len(r.Untracked) > 0) && !discard {
		return e, fmt.Errorf("review local files and supply --discard-reviewed for all mode")
	}
	if inside(r.Path, store.Dir) {
		return e, fmt.Errorf("journal must be outside the checkout")
	}
	if mode == "backup" {
		if backup == "" {
			return e, fmt.Errorf("backup mode needs --backup")
		}
		p, err := canonicalDestination(backup)
		if err != nil {
			return e, err
		}
		if inside(r.Path, p) {
			return e, fmt.Errorf("backup must be outside checkout")
		}
		e.Backup = filepath.Join(p, e.ID)
		if exists(e.Backup) {
			return e, fmt.Errorf("backup destination exists")
		}
	}
	// Local state must match what was reviewed after verification.
	fresh := Inspect(ctx, r.Path, false, false)
	if fresh.Fingerprint != r.Fingerprint {
		return e, fmt.Errorf("repository changed after preview; review again")
	}
	if r.FileSnapshot != "" {
		now, err := metadataDigest(ctx, r.Path)
		if err != nil {
			return e, err
		}
		if now != r.FileSnapshot {
			return e, fmt.Errorf("filesystem changed after review")
		}
	}
	if len(fresh.Errors) > 0 {
		return e, fmt.Errorf("reinspection failed")
	}
	e.Stage = filepath.Join(filepath.Dir(r.Path), ".git-shed-stage-"+e.ID)
	if exists(e.Stage) {
		return e, fmt.Errorf("staging path exists")
	}
	payload := filepath.Join(e.Stage, "checkout")
	if mode == "all" {
		e.Moves = []Move{{r.Path, payload}}
	} else {
		for _, rel := range r.Tracked {
			p, err := safePath(r.Path, rel)
			if err != nil {
				return e, err
			}
			if !exists(p) {
				return e, fmt.Errorf("tracked path disappeared: %s", p)
			}
			e.Moves = append(e.Moves, Move{p, filepath.Join(payload, rel)})
		}
		e.Moves = append(e.Moves, Move{filepath.Join(r.Path, ".git"), filepath.Join(payload, ".git")})
	}
	if err := store.save(&e); err != nil {
		return e, err
	}
	if err := os.Mkdir(e.Stage, 0700); err != nil {
		return fail(err)
	}
	e.State = "moving"
	if err := store.save(&e); err != nil {
		return fail(err)
	}
	for _, m := range e.Moves {
		if ctx.Err() != nil {
			return fail(ctx.Err())
		}
		if err := renameNoClobber(m.From, m.To); err != nil {
			return fail(err)
		}
	}
	if mode == "backup" {
		// After tracked paths and metadata leave, every remaining file is local-only.
		var paths []string
		err := filepath.WalkDir(r.Path, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() {
				paths = append(paths, p)
			}
			return nil
		})
		if err != nil {
			return fail(err)
		}
		for _, p := range paths {
			rel, _ := filepath.Rel(r.Path, p)
			e.BackupMoves = append(e.BackupMoves, Move{p, filepath.Join(e.Backup, rel)})
		}
		if err := store.save(&e); err != nil {
			return fail(err)
		}
		for _, m := range e.BackupMoves {
			if err := moveVerified(ctx, m.From, m.To); err != nil {
				return fail(err)
			}
		}
	}
	if mode != "all" {
		if mode == "leave" {
			pruneEmptyExcept(r.Path, append(append([]string(nil), r.Ignored...), r.Untracked...))
		} else {
			pruneEmpty(r.Path)
		}
	}
	e.State = "staged"
	digest, err := treeDigest(ctx, e.Stage)
	if err != nil {
		return fail(err)
	}
	e.Digest = digest
	_, e.Bytes, err = treeSize(ctx, e.Stage)
	if err != nil {
		return fail(err)
	}
	if err = store.save(&e); err != nil {
		return fail(err)
	}
	e.State = "trashing"
	if err = store.save(&e); err != nil {
		return fail(err)
	}
	e.Trash, e.Info, err = systemTrash(e.Stage)
	if err != nil {
		return fail(err)
	}
	e.State = "trashed"
	if err = store.save(&e); err != nil {
		return fail(err)
	}
	return e, nil
}
func pruneEmpty(root string) {
	var dirs []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, e error) error {
		if e == nil && d.IsDir() {
			dirs = append(dirs, p)
		}
		return nil
	})
	for i := len(dirs) - 1; i >= 0; i-- {
		_ = os.Remove(dirs[i])
	}
}
func moveVerified(ctx context.Context, from, to string) error {
	if exists(to) {
		return fmt.Errorf("backup collision: %s", to)
	}
	if e := safeAncestors(to); e != nil {
		return e
	}
	if e := os.MkdirAll(filepath.Dir(to), 0700); e != nil {
		return e
	}
	if e := os.Rename(from, to); e == nil {
		return nil
	}
	i, e := os.Lstat(from)
	if e != nil {
		return e
	}
	if i.Mode()&os.ModeSymlink != 0 {
		target, e := os.Readlink(from)
		if e != nil {
			return e
		}
		if e = os.Symlink(target, to); e != nil {
			return e
		}
		return os.Remove(from)
	}
	if !i.Mode().IsRegular() {
		return fmt.Errorf("cannot back up special file %s", from)
	}
	f, e := os.Open(from)
	if e != nil {
		return e
	}
	defer f.Close()
	out, e := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, i.Mode().Perm())
	if e != nil {
		return e
	}
	_, e = io.Copy(out, f)
	if e == nil {
		e = out.Sync()
	}
	ce := out.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		_ = os.Remove(to)
		return e
	}
	a, e := fileHash(from)
	if e != nil {
		return e
	}
	b, e := fileHash(to)
	if e != nil {
		return e
	}
	if a != b {
		return fmt.Errorf("backup verification failed")
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	_ = os.Chtimes(to, i.ModTime(), i.ModTime())
	return os.Remove(from)
}
func fileHash(p string) (string, error) {
	f, e := os.Open(p)
	if e != nil {
		return "", e
	}
	defer f.Close()
	h := sha256.New()
	_, e = io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), e
}
func locatePayload(e Entry) (string, error) {
	if e.Trash != "" && exists(e.Trash) {
		return e.Trash, nil
	}
	if exists(e.Stage) {
		return e.Stage, nil
	}
	return "", fmt.Errorf("payload not found; inspect system Trash for .git-shed-stage-%s and set its path with restore --payload", e.ID)
}
func Restore(ctx context.Context, s Store, e *Entry, payload string) error {
	if e.State == "purged" || e.State == "restored" {
		return fmt.Errorf("entry already %s", e.State)
	}
	var err error
	if payload == "" {
		payload, err = locatePayload(*e)
		if err != nil {
			return err
		}
	} else {
		payload, err = canonical(payload)
		if err != nil {
			return err
		}
		if filepath.Base(payload) != filepath.Base(e.Stage) {
			return fmt.Errorf("payload name does not match journal")
		}
	}
	mapPath := func(p string) string { rel, _ := filepath.Rel(e.Stage, p); return filepath.Join(payload, rel) }
	// Preflight every collision before changing any file, including interrupted moves.
	var moves []Move
	for _, m := range e.Moves {
		from := mapPath(m.To)
		if exists(from) {
			if exists(m.From) {
				return fmt.Errorf("restore collision: %s", m.From)
			}
			moves = append(moves, Move{from, m.From})
		} else if !exists(m.From) {
			return fmt.Errorf("missing original and staged path: %s", m.From)
		}
	}
	for _, m := range e.BackupMoves {
		if exists(m.To) {
			if exists(m.From) {
				return fmt.Errorf("restore collision: %s", m.From)
			}
			moves = append(moves, Move{m.To, m.From})
		} else if !exists(m.From) {
			return fmt.Errorf("missing backup: %s", m.To)
		}
	}
	e.State = "restoring"
	if err = s.save(e); err != nil {
		return err
	}
	for _, m := range moves {
		if err = moveVerified(ctx, m.From, m.To); err != nil {
			e.Error = err.Error()
			_ = s.save(e)
			return err
		}
	}
	pruneEmpty(payload)
	if e.Backup != "" {
		pruneEmpty(e.Backup)
	}
	if e.Info != "" {
		if err = os.Remove(e.Info); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	e.State = "restored"
	e.Error = ""
	return s.save(e)
}
func Purge(ctx context.Context, s Store, e *Entry) error {
	if e.State != "trashed" {
		return fmt.Errorf("only completely trashed entries can be purged")
	}
	payload, err := locatePayload(*e)
	if err != nil {
		return err
	}
	if filepath.Base(payload) != ".git-shed-stage-"+e.ID {
		return fmt.Errorf("payload is not a managed Trash entry")
	}
	if payload != e.Trash {
		return fmt.Errorf("entry has not reached system Trash")
	}
	info, err := os.Lstat(payload)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("unsafe payload")
	}
	digest, err := treeDigest(ctx, payload)
	if err != nil {
		return err
	}
	if digest != e.Digest {
		return fmt.Errorf("Trash contents changed; refusing purge")
	}
	r := Inspect(ctx, filepath.Join(payload, "checkout"), false, true)
	if len(r.Blockers) > 0 {
		return fmt.Errorf("remote recheck blocked purge: %s", strings.Join(r.Blockers, "; "))
	}
	if r.RecoveryCommits > 0 {
		return fmt.Errorf("%d reflog-only recovery commits remain; restore and preserve them before purge", r.RecoveryCommits)
	}
	// Verification must not have changed payload bytes.
	after, err := treeDigest(ctx, payload)
	if err != nil {
		return err
	}
	if after != digest {
		return fmt.Errorf("payload changed during verification")
	}
	e.State = "purging"
	if err = s.save(e); err != nil {
		return err
	}
	if err = os.RemoveAll(payload); err != nil {
		return err
	}
	if e.Info != "" {
		if err = os.Remove(e.Info); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	e.State = "purged"
	return s.save(e)
}
func metadataDigest(ctx context.Context, root string) (string, error) {
	h := sha256.New()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, e error) error {
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
		rel, _ := filepath.Rel(root, p)
		fmt.Fprintf(h, "%q %v %d %d\x00", rel, i.Mode(), i.Size(), i.ModTime().UnixNano())
		if i.Mode()&os.ModeSymlink != 0 {
			target, e := os.Readlink(p)
			if e != nil {
				return e
			}
			fmt.Fprintf(h, "%q\x00", target)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func pruneEmptyExcept(root string, keep []string) {
	protected := map[string]bool{}
	for _, rel := range keep {
		p := filepath.Join(root, rel)
		for inside(root, p) {
			protected[p] = true
			if p == root {
				break
			}
			p = filepath.Dir(p)
		}
	}
	var dirs []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, e error) error {
		if e == nil && d.IsDir() {
			dirs = append(dirs, p)
		}
		return nil
	})
	for i := len(dirs) - 1; i >= 0; i-- {
		if !protected[dirs[i]] {
			_ = os.Remove(dirs[i])
		}
	}
}
