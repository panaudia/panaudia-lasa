//go:build linux || darwin

package main

import (
	"net"

	"golang.org/x/sys/unix"
)

// readUDPBuffers reports the kernel's view of the socket buffers.
func readUDPBuffers(conn *net.UDPConn) udpBuffers {
	var b udpBuffers
	rc, err := conn.SyscallConn()
	if err != nil {
		return b
	}
	_ = rc.Control(func(fd uintptr) {
		b.RecvBytes, _ = unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_RCVBUF)
		b.SendBytes, _ = unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_SNDBUF)
	})
	b.Sufficient = b.RecvBytes >= sufficientUDPBuffer && b.SendBytes >= sufficientUDPBuffer
	return b
}
