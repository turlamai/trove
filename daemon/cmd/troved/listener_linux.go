//go:build linux

// Verified Unix listener: rejects any connection whose peer credentials do
// not match the daemon's own (root). Together with the per-start token this
// is the daemon's gateway authentication (spec §8.2).
package main

import (
	"fmt"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

type verifiedListener struct {
	inner net.Listener
	uid   uint32
}

func (l *verifiedListener) Accept() (net.Conn, error) {
	for {
		c, err := l.inner.Accept()
		if err != nil {
			return nil, err
		}
		uc, ok := c.(*net.UnixConn)
		if !ok {
			c.Close()
			continue
		}
		if !l.peerOK(uc) {
			c.Close()
			continue // refuse silently: not even a valid client
		}
		return c, nil
	}
}

func (l *verifiedListener) peerOK(c *net.UnixConn) bool {
	rc, err := c.SyscallConn()
	if err != nil {
		return false
	}
	var ok bool
	var ucred *unix.Ucred
	err = rc.Control(func(fd uintptr) {
		ucred, err = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if err == nil {
			ok = ucred.Uid == l.uid
		}
	})
	if err != nil {
		return false
	}
	return ok
}

func (l *verifiedListener) Close() error   { return l.inner.Close() }
func (l *verifiedListener) Addr() net.Addr { return l.inner.Addr() }

func listenUnixVerified(path string) (net.Listener, error) {
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", path, err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		ln.Close()
		return nil, err
	}
	return &verifiedListener{inner: ln, uid: uint32(os.Geteuid())}, nil
}
