//go:build linux

/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package proxy

import (
	"fmt"
	"net"
	"syscall"
	"unsafe"
)

// SO_ORIGINAL_DST is the Linux socket option that exposes the pre-NAT
// destination of a connection that was caught by iptables REDIRECT.
// Not in the stdlib's syscall package, but the constant is stable:
// include/uapi/linux/netfilter_ipv4.h.
const soOriginalDst = 80

// originalDestination recovers (IP, port) from an iptables-redirected
// inbound connection. Linux-only. Caller passes a *net.TCPConn whose
// remote end terminated inside the proxy listener; the returned tuple
// is the upstream the kernel would have routed to had REDIRECT not
// intervened.
func originalDestination(conn net.Conn) (net.IP, int, error) {
	tc, ok := conn.(*net.TCPConn)
	if !ok {
		return nil, 0, fmt.Errorf("SO_ORIGINAL_DST requires *net.TCPConn, got %T", conn)
	}
	raw, err := tc.SyscallConn()
	if err != nil {
		return nil, 0, fmt.Errorf("SyscallConn: %w", err)
	}
	var sa syscall.RawSockaddrInet4
	var getErr error
	controlErr := raw.Control(func(fd uintptr) {
		size := uint32(unsafe.Sizeof(sa))
		_, _, errno := syscall.Syscall6(
			syscall.SYS_GETSOCKOPT,
			fd,
			syscall.IPPROTO_IP,
			soOriginalDst,
			uintptr(unsafe.Pointer(&sa)),
			uintptr(unsafe.Pointer(&size)),
			0,
		)
		if errno != 0 {
			getErr = fmt.Errorf("getsockopt SO_ORIGINAL_DST: %w", errno)
		}
	})
	if controlErr != nil {
		return nil, 0, controlErr
	}
	if getErr != nil {
		return nil, 0, getErr
	}
	// sa.Port is stored big-endian in the sockaddr; decode explicitly.
	port := int(sa.Port>>8)&0xff | int(sa.Port&0xff)<<8
	ip := net.IPv4(sa.Addr[0], sa.Addr[1], sa.Addr[2], sa.Addr[3])
	return ip, port, nil
}

// TransparentInterceptionSupported reports whether the current build
// has a working SO_ORIGINAL_DST implementation. True on Linux, false
// elsewhere. Used by cmd/proxy to fail fast on non-Linux builds when
// --mode=transparent is selected.
func TransparentInterceptionSupported() bool { return true }
