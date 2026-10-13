//go:build (!darwin && !linux) || (darwin && !cgo)

package main

import "fmt"

func systemTrash(path string) (string, string, error) {
	return "", "", fmt.Errorf("system Trash requires Linux or a macOS build with CGO_ENABLED=1")
}
