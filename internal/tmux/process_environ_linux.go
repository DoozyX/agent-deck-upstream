//go:build linux

package tmux

import (
	"fmt"
	"os"
	"strings"
)

// sameMountNamespace reports whether pid resolves filesystem paths in this
// process's Linux mount namespace. An unreadable namespace is fail-closed.
func sameMountNamespace(pid int) bool {
	theirs, err := os.Readlink(fmt.Sprintf("/proc/%d/ns/mnt", pid))
	if err != nil {
		return false
	}
	ours, err := os.Readlink("/proc/self/ns/mnt")
	return err == nil && theirs == ours
}

func readProcessEnviron(pid int) (func(string) string, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
	if err != nil {
		return nil, err
	}
	return processEnvironLookup(pid, strings.FieldsFunc(string(raw), func(r rune) bool { return r == 0 }))
}
