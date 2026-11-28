//go:build cosmo

// host_cosmo.go holds onDarwin.

package pty

import "runtime"

// onDarwin is true when the fat binary runs on macOS. The fork passes Apple's pty requests through, and no Linux one.
var onDarwin = runtime.CosmoHostOS() == "darwin"
