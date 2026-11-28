//go:build !cosmo

// host_other.go holds onDarwin.

package pty

import "runtime"

// onDarwin is true for a native macOS build.
var onDarwin = runtime.GOOS == "darwin"
