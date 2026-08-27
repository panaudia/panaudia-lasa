package main

// UDP socket buffers for the one QUIC listener.
//
// quic-go wants 7 MiB each way (its DesiredReceiveBufferSize); Linux
// caps an unprivileged setsockopt at net.core.rmem_max / wmem_max,
// 208 kiB on a stock kernel, and net.core.* is not a namespaced sysctl
// so a container cannot raise it. The kernel charges the buffer by
// skb truesize (~2 kiB per datagram regardless of payload), so at the
// default a 64-entity space has roughly 60 ms of queue before packets
// drop when the process is descheduled; at 7 MiB, about a second.
//
// Two ways past the cap, and this file handles both:
//   - the host sets the sysctl (the real fix, documented in the README);
//   - the process holds CAP_NET_ADMIN, which lets SO_RCVBUFFORCE ignore
//     the cap. The image sets cap_net_admin+p on the binary (permitted,
//     not effective, so exec never fails), and `--cap-add NET_ADMIN`
//     at run time puts it in the process's permitted set. udpPrelude
//     (udpbuf_linux.go) then binds the socket, forces the sizes, and
//     re-execs the binary with the socket inherited and no_new_privs
//     set, so the process that actually serves holds no capability on
//     any thread. (Capabilities are per thread and this binary is cgo,
//     so dropping in place would only ever cover one thread.)
//
// Whatever happens, the effective sizes are logged at startup (WARN
// with the remedies when short) and exposed in /stats as "udp".

import (
	"fmt"
	"log/slog"
	"net"
	"os"
)

// desiredUDPBuffer mirrors quic-go's protocol.DesiredReceiveBufferSize
// / DesiredSendBufferSize (internal, so restated here).
const desiredUDPBuffer = 7 << 20

type udpBuffers struct {
	RecvBytes  int  `json:"recv_bytes"`
	SendBytes  int  `json:"send_bytes"`
	Forced     bool `json:"forced"`     // CAP_NET_ADMIN was used to exceed the sysctl cap
	Sufficient bool `json:"sufficient"` // both sides at quic-go's desired size
}

// preludeConn is the socket udpPrelude bound and tuned when it could
// not hand it over by re-exec (exec failed): listenUDP adopts it.
var preludeConn *net.UDPConn

// listenUDP is the media socket for newApp: the one inherited from the
// re-exec handoff when there is one, else the prelude's, else a fresh
// bind sized as far as the host allows. quic-go sets the same sizes
// again when it wraps the socket and stays quiet when they already
// hold.
func listenUDP(cfg appConfig) (*net.UDPConn, udpBuffers, error) {
	if fd := inheritedUDPFD(); fd >= 0 {
		f := os.NewFile(uintptr(fd), "udp")
		pc, err := net.FilePacketConn(f)
		_ = f.Close() // FilePacketConn dup'd it
		if err != nil {
			return nil, udpBuffers{}, fmt.Errorf("inherited udp socket: %w", err)
		}
		conn, ok := pc.(*net.UDPConn)
		if !ok {
			_ = pc.Close()
			return nil, udpBuffers{}, fmt.Errorf("inherited fd %d is not a udp socket", fd)
		}
		b := readUDPBuffers(conn)
		b.Forced = b.Sufficient
		return conn, b, nil
	}
	if conn := preludeConn; conn != nil {
		preludeConn = nil
		b := readUDPBuffers(conn)
		b.Forced = b.Sufficient
		return conn, b, nil
	}
	conn, err := bindUDP(cfg)
	if err != nil {
		return nil, udpBuffers{}, err
	}
	return conn, readUDPBuffers(conn), nil
}

// bindUDP binds the configured address and asks for the desired sizes
// the unprivileged way; the kernel clamps to its sysctl.
func bindUDP(cfg appConfig) (*net.UDPConn, error) {
	addr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", cfg.Host, cfg.Port))
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, err
	}
	_ = conn.SetReadBuffer(desiredUDPBuffer)
	_ = conn.SetWriteBuffer(desiredUDPBuffer)
	return conn, nil
}

func (b udpBuffers) log() {
	if b.Sufficient {
		via := "host sysctl"
		if b.Forced {
			via = "CAP_NET_ADMIN"
		}
		slog.Info("panaudia-server: udp buffers", "recvKiB", b.RecvBytes/1024, "sendKiB", b.SendBytes/1024, "via", via)
		return
	}
	slog.Warn("panaudia-server: udp buffers below the QUIC target; bursts will drop packets under load",
		"recvKiB", b.RecvBytes/1024, "sendKiB", b.SendBytes/1024, "wantKiB", sufficientUDPBuffer/1024,
		"remedy", "run the container with --cap-add NET_ADMIN (Kubernetes: securityContext.capabilities.add: [NET_ADMIN]), "+
			"or on the host: sysctl -w net.core.rmem_max=7500000 net.core.wmem_max=7500000")
}
