//go:build !windows

package cli

import "syscall"

func readIPv6Only(fd uintptr) (int, error) {
	return syscall.GetsockoptInt(int(fd), syscall.IPPROTO_IPV6, syscall.IPV6_V6ONLY)
}
