//go:build linux

package main

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func device(p string) (uint64, error) {
	var st syscall.Stat_t
	e := syscall.Stat(p, &st)
	return uint64(st.Dev), e
}
func privateTrashDir(p string) error {
	if e := safeAncestors(filepath.Join(p, "child")); e != nil {
		return e
	}
	if e := os.MkdirAll(p, 0700); e != nil {
		return e
	}
	i, e := os.Lstat(p)
	if e != nil {
		return e
	}
	st, ok := i.Sys().(*syscall.Stat_t)
	if !ok || !i.IsDir() || i.Mode()&os.ModeSymlink != 0 || st.Uid != uint32(os.Getuid()) || i.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("unsafe Trash directory: %s", p)
	}
	return nil
}
func systemTrash(path string) (string, string, error) {
	home, e := os.UserHomeDir()
	if e != nil {
		return "", "", e
	}
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		base = filepath.Join(home, ".local", "share")
	}
	if !filepath.IsAbs(base) {
		return "", "", fmt.Errorf("XDG_DATA_HOME must be absolute")
	}
	if e = os.MkdirAll(base, 0700); e != nil {
		return "", "", e
	}
	sourceDev, e := device(path)
	if e != nil {
		return "", "", e
	}
	homeDev, e := device(base)
	if e != nil {
		return "", "", e
	}
	trash := filepath.Join(base, "Trash")
	original := path
	if sourceDev != homeDev {
		top := filepath.Dir(path)
		for {
			parent := filepath.Dir(top)
			if parent == top {
				break
			}
			dev, e := device(parent)
			if e != nil {
				return "", "", e
			}
			if dev != sourceDev {
				break
			}
			top = parent
		}
		shared := filepath.Join(top, ".Trash")
		i, e := os.Lstat(shared)
		valid := e == nil && i.IsDir() && i.Mode()&os.ModeSymlink == 0 && i.Mode()&os.ModeSticky != 0
		trash = filepath.Join(top, ".Trash-"+strconv.Itoa(os.Getuid()))
		if valid {
			candidate := filepath.Join(shared, strconv.Itoa(os.Getuid()))
			if privateTrashDir(candidate) == nil {
				trash = candidate
			}
		}
		original, e = filepath.Rel(top, path)
		if e != nil {
			return "", "", e
		}
	}
	for _, p := range []string{trash, filepath.Join(trash, "files"), filepath.Join(trash, "info")} {
		if e = privateTrashDir(p); e != nil {
			return "", "", e
		}
	}
	name := filepath.Base(path)
	dest := filepath.Join(trash, "files", name)
	info := filepath.Join(trash, "info", name+".trashinfo")
	if exists(dest) || exists(info) {
		return "", "", fmt.Errorf("Trash name collision")
	}
	escaped := (&url.URL{Path: original}).EscapedPath()
	if strings.ContainsAny(escaped, "\r\n") {
		return "", "", fmt.Errorf("unsafe Trash metadata")
	}
	f, e := os.OpenFile(info, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return "", "", e
	}
	_, e = fmt.Fprintf(f, "[Trash Info]\nPath=%s\nDeletionDate=%s\n", escaped, time.Now().Format("2006-01-02T15:04:05"))
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		_ = os.Remove(info)
		return "", "", e
	}
	if e = os.Rename(path, dest); e != nil {
		_ = os.Remove(info)
		return "", "", e
	}
	return dest, info, nil
}
