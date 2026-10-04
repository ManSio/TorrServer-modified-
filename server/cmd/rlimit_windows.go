//go:build windows
// +build windows

package main

func setFileDescriptorLimit() {
	// Windows doesn't use ulimit - no-op
}
