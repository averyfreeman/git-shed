//go:build darwin && cgo

package main

/*
#cgo LDFLAGS: -framework Foundation
#include <stdlib.h>
char *shed_trash(const char *path, char **error);
*/
import "C"
import (
	"fmt"
	"unsafe"
)

func systemTrash(path string) (string, string, error) {
	p := C.CString(path)
	defer C.free(unsafe.Pointer(p))
	var err *C.char
	result := C.shed_trash(p, &err)
	if err != nil {
		defer C.free(unsafe.Pointer(err))
		return "", "", fmt.Errorf("system Trash: %s", C.GoString(err))
	}
	if result == nil {
		return "", "", fmt.Errorf("system Trash returned no destination")
	}
	defer C.free(unsafe.Pointer(result))
	return C.GoString(result), "", nil
}
