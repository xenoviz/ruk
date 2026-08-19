//go:build windows

package cli

import "syscall"

func setIPv6Only(fd uintptr, only bool) error {
	value := 0
	if only {
		value = 1
	}
	return syscall.SetsockoptInt(syscall.Handle(fd), syscall.IPPROTO_IPV6, syscall.IPV6_V6ONLY, value)
}
