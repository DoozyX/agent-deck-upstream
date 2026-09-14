//go:build darwin

package tmux

import (
	"encoding/binary"
	"fmt"
	"strings"

	"golang.org/x/sys/unix"
)

// macOS has no per-process mount namespaces. A readable kern.procargs2 record
// below is therefore sufficient to establish that the candidate resolves the
// same filesystem paths as this process.
func sameMountNamespace(pid int) bool {
	return pid > 0
}

func readProcessEnviron(pid int) (func(string) string, error) {
	raw, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return nil, err
	}
	envRaw, err := darwinProcessEnviron(raw)
	if err != nil {
		return nil, fmt.Errorf("process %d environment: %w", pid, err)
	}
	return processEnvironLookup(pid, envRaw)
}

func darwinProcessEnviron(raw []byte) ([]string, error) {
	if len(raw) < 4 {
		return nil, fmt.Errorf("short kern.procargs2 record")
	}
	argc := int(binary.LittleEndian.Uint32(raw[:4]))
	if argc < 0 {
		return nil, fmt.Errorf("invalid argc %d", argc)
	}
	rest := raw[4:]
	pathEnd := strings.IndexByte(string(rest), 0)
	if pathEnd < 0 {
		return nil, fmt.Errorf("missing executable terminator")
	}
	rest = rest[pathEnd+1:]
	for len(rest) > 0 && rest[0] == 0 {
		rest = rest[1:]
	}
	for n := 0; n < argc; n++ {
		end := strings.IndexByte(string(rest), 0)
		if end < 0 {
			return nil, fmt.Errorf("missing argv terminator")
		}
		rest = rest[end+1:]
	}
	entries := strings.Split(string(rest), "\x00")
	environ := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry == "" {
			break
		}
		environ = append(environ, entry)
	}
	return environ, nil
}
