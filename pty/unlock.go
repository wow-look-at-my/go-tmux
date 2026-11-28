// unlock.go holds appleTIOCPTYGRANT.

package pty

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strconv"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Apple's pty requests, from the Go tree's syscall/zerrors_darwin_arm64.go. A cosmo build has no darwin constants to
// name them by.
const (
	appleTIOCPTYGRANT = 0x20007454
	appleTIOCPTYUNLK  = 0x20007452
	appleTIOCPTYGNAME = 0x40807453
	appleTIOCSWINSZ   = 0x80087467
	appleTIOCGPGRP    = 0x40047477
)

// unlock makes the other end of the terminal openable and reports its path.
func unlock(master *os.File) (string, error) {
	if onDarwin {
		return unlockDarwin(master)
	}
	return unlockLinux(master)
}

func unlockLinux(master *os.File) (string, error) {
	var name string
	err := control(master, func(fd int) error {
		if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
			return fmt.Errorf("pty: unlock the terminal: %w", err)
		}
		n, err := unix.IoctlGetUint32(fd, unix.TIOCGPTN)
		if err != nil {
			return fmt.Errorf("pty: read the terminal's number: %w", err)
		}
		name = "/dev/pts/" + strconv.FormatUint(uint64(n), 10)
		return nil
	})
	return name, err
}

// unlockDarwin does what grantpt, unlockpt and ptsname do in Apple's libc.
func unlockDarwin(master *os.File) (string, error) {
	err := control(master, func(fd int) error {
		if err := ioctl(fd, appleTIOCPTYGRANT, 0); err != nil {
			return fmt.Errorf("pty: grant the terminal: %w", err)
		}
		if err := ioctl(fd, appleTIOCPTYUNLK, 0); err != nil {
			return fmt.Errorf("pty: unlock the terminal: %w", err)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return terminalName(master)
}

// terminalName is the path of the other end of the terminal.
func terminalName(master *os.File) (string, error) {
	var name string
	err := control(master, func(fd int) error {
		// The request writes a NUL-terminated path into a buffer of many bytes.
		var buf [128]byte
		if err := ioctl(fd, appleTIOCPTYGNAME, uintptr(unsafe.Pointer(&buf[0]))); err != nil {
			return fmt.Errorf("pty: read the terminal's name: %w", err)
		}
		n := bytes.IndexByte(buf[:], 0)
		if n <= 0 {
			return errors.New("pty: read the terminal's name: the kernel gave an empty name")
		}
		name = string(buf[:n])
		return nil
	})
	return name, err
}

// farEnd opens the other end of the terminal.
func farEnd(master *os.File) (*os.File, error) {
	name, err := terminalName(master)
	if err != nil {
		return nil, err
	}
	slave, err := os.OpenFile(name, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, fmt.Errorf("pty: open %s: %w", name, err)
	}
	return slave, nil
}

// setSizeDarwin sets the size on the other end, the end a darwin host takes the request on.
func setSizeDarwin(master *os.File, ws *unix.Winsize) error {
	slave, err := farEnd(master)
	if err != nil {
		return err
	}
	defer slave.Close()
	return control(slave, func(fd int) error {
		return ioctl(fd, appleTIOCSWINSZ, uintptr(unsafe.Pointer(ws)))
	})
}

// foregroundDarwin reads the foreground process group on the controlling end.
func foregroundDarwin(master *os.File) (int, error) {
	var pgrp int32
	err := control(master, func(fd int) error {
		return ioctl(fd, appleTIOCGPGRP, uintptr(unsafe.Pointer(&pgrp)))
	})
	return int(pgrp), err
}

func ioctl(fd int, req, arg uintptr) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), req, arg)
	if errno != 0 {
		return errno
	}
	return nil
}
