//go:build !windows
// +build !windows

package main

import "syscall"

func setFileDescriptorLimit() {
	// Повышаем ulimit открытых файлов (File Descriptors) до 65535 при старте
	var rLimit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rLimit); err == nil {
		rLimit.Max = 65535
		rLimit.Cur = 65535
		if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &rLimit); err != nil {
			// Если жесткий лимит зажат ОС, поднимаем мягкий Cur до текущего системного максимума Max
			rLimit.Cur = rLimit.Max
			_ = syscall.Setrlimit(syscall.RLIMIT_NOFILE, &rLimit)
		}
	}
}
