//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package flowcli

import "syscall"

func makeTestFIFO(path string, mode uint32) error {
	return syscall.Mkfifo(path, mode)
}
