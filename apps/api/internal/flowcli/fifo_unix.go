//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package flowcli

import "syscall"

func unblockNamedPipeOpen(path string) {
	descriptor, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err == nil {
		_ = syscall.Close(descriptor)
	}
}
