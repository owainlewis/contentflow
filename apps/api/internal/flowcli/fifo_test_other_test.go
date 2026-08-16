//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package flowcli

import "errors"

func makeTestFIFO(string, uint32) error {
	return errors.New("named pipes are unsupported")
}
