//go:build linux

package repository

import "syscall"

// tcpUserTimeout is TCP_USER_TIMEOUT from Linux include/uapi/linux/tcp.h; the
// standard library's syscall package does not define it on linux/amd64.
const tcpUserTimeout = 0x12

// upstreamDialControl sets TCP_USER_TIMEOUT on each upstream socket
// before it connects.
func upstreamDialControl(_, _ string, raw syscall.RawConn) error {
	var sockErr error
	if err := raw.Control(func(fd uintptr) {
		sockErr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP, tcpUserTimeout, int(upstreamTCPUserTimeout.Milliseconds()))
	}); err != nil {
		return err
	}
	return sockErr
}
