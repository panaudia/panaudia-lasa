package main

import (
	"net"
	"os"
	"runtime"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

// Linux reports back 2× what was set (it counts its own overhead).
const sufficientUDPBuffer = 2 * desiredUDPBuffer

const capNetAdmin = 12 // CAP_NET_ADMIN, linux/capability.h

// udpPrelude runs first thing in main, before the logger: if the
// process holds CAP_NET_ADMIN permitted (the image's file capability
// plus --cap-add at run time), bind the media socket, force its
// buffers past the sysctl cap, and re-exec this binary with the socket
// as an inherited fd and no capability anywhere in the new process.
//
// The exec is what makes the drop real. Capabilities are per thread,
// the Go runtime already has several, and syscall.AllThreadsSyscall
// refuses cgo binaries — so instead: capset(0) on this (locked) thread,
// PR_SET_NO_NEW_PRIVS on it (needs no privilege; exec then computes
// permitted = fileCaps ∩ oldPermitted = ∅), and execve carries exactly
// this thread's credentials into the fresh image. The child sees the
// PANAUDIA_INHERITED_UDP_FD handoff (config.go) and adopts the socket.
//
// Not permitted, or anything fails before the exec: return and let
// the ordinary path bind and report. Exec itself failing after the
// socket is tuned: keep the socket (preludeConn) and carry on in this
// process — buffers forced, capability lowered on this thread only.
func udpPrelude(cfg appConfig) {
	if inheritedUDPFD() >= 0 {
		return // we are the child
	}
	runtime.LockOSThread() // capset, setsockopt and execve must share a thread
	defer runtime.UnlockOSThread()
	if !raiseNetAdmin() {
		return
	}
	conn, err := bindUDP(cfg)
	if err != nil {
		dropThreadCaps()
		return // the ordinary path reports the bind error
	}
	if forceUDPBuffers(conn, desiredUDPBuffer) != nil {
		_ = conn.Close()
		dropThreadCaps()
		return
	}
	dropThreadCaps()
	if !readUDPBuffers(conn).Sufficient {
		preludeConn = conn
		return
	}
	// Hand the socket over without close-on-exec, then become the child.
	rc, err := conn.SyscallConn()
	if err != nil {
		preludeConn = conn
		return
	}
	var nfd int = -1
	_ = rc.Control(func(fd uintptr) {
		if d, err := unix.Dup(int(fd)); err == nil {
			if _, err := unix.FcntlInt(uintptr(d), unix.F_SETFD, 0); err == nil {
				nfd = d
			} else {
				_ = unix.Close(d)
			}
		}
	})
	if nfd < 0 {
		preludeConn = conn
		return
	}
	exe, err := os.Executable()
	if err == nil {
		err = unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0)
	}
	if err == nil {
		err = syscall.Exec(exe, os.Args, environWith(inheritedUDPFDVar, strconv.Itoa(nfd)))
	}
	// Only reached when exec failed.
	_ = unix.Close(nfd)
	preludeConn = conn
}

// raiseNetAdmin moves CAP_NET_ADMIN from the permitted to the
// effective set of the calling thread, if it is there to move. False
// in the plain unprivileged case.
func raiseNetAdmin() bool {
	hdr := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	var data [2]unix.CapUserData
	if err := unix.Capget(&hdr, &data[0]); err != nil {
		return false
	}
	if data[0].Permitted&(1<<capNetAdmin) == 0 {
		return false
	}
	data[0].Effective |= 1 << capNetAdmin
	return unix.Capset(&hdr, &data[0]) == nil
}

// dropThreadCaps empties the calling thread's permitted, effective and
// inheritable sets. Per thread by kernel design; the exec in udpPrelude
// is what extends it to the whole process.
func dropThreadCaps() {
	hdr := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	var data [2]unix.CapUserData
	_ = unix.Capset(&hdr, &data[0])
}

// forceUDPBuffers is SO_RCVBUFFORCE / SO_SNDBUFFORCE: applied regardless
// of net.core.rmem_max / wmem_max. Needs CAP_NET_ADMIN effective on the
// calling thread, else EPERM.
func forceUDPBuffers(conn *net.UDPConn, bytes int) error {
	rc, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var serr error
	if err := rc.Control(func(fd uintptr) {
		if serr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_RCVBUFFORCE, bytes); serr != nil {
			return
		}
		serr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_SNDBUFFORCE, bytes)
	}); err != nil {
		return err
	}
	return serr
}
