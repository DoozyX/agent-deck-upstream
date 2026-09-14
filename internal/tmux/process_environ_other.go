//go:build !linux && !darwin

package tmux

import "fmt"

func sameMountNamespace(int) bool { return false }

func readProcessEnviron(pid int) (func(string) string, error) {
	return nil, fmt.Errorf("process environment lookup unsupported for pid %d", pid)
}
