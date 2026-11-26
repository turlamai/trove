//go:build !linux

// Non-Linux builds are development-only: no peer credential check exists on
// these platforms, so the socket is chmod 0600 and the docs say plainly that
// production runs on Linux.
package main

import (
	"fmt"
	"net"
	"os"
)

func listenUnixVerified(path string) (net.Listener, error) {
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", path, err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}
