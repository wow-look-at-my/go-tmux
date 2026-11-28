// Package pty starts a program on a pseudo-terminal and gives the caller the controlling end.
package pty

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

// Command says what to start on the terminal.
type Command struct {
	// Path is the program. Args are its arguments, without the program name.
	Path string
	Args []string
	// Argv0 is the name the program sees itself started as. Empty is Path.
	Argv0 string
	// Env is the whole environment of the child.
	Env []string
	// Dir is the working directory. Empty keeps the caller's.
	Dir string
	// Cols and Rows are the size the terminal starts at.
	Cols, Rows int
}

// Pty is a running child and the controlling end of its terminal.
type Pty struct {
	master *os.File
	cmd    *exec.Cmd

	done chan struct{}
	// status is valid once done is closed.
	status int

	closeOnce sync.Once
}

// Start opens a terminal at the given size and starts the command on it, as the leader of a new session.
func Start(c Command) (*Pty, error) {
	master, slave, err := Open(c.Cols, c.Rows)
	if err != nil {
		return nil, err
	}
	defer slave.Close()
	p := &Pty{master: master, done: make(chan struct{})}

	cmd := exec.Command(c.Path, c.Args...)
	if c.Argv0 != "" {
		cmd.Args[0] = c.Argv0
	}
	cmd.Env = c.Env
	cmd.Dir = c.Dir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	// Ctty names the child's descriptor, and standard input is the terminal there.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		master.Close()
		return nil, fmt.Errorf("pty: start %s: %w", c.Path, err)
	}
	p.cmd = cmd
	go p.wait()
	return p, nil
}

// Open opens a terminal of the given size with no program on it. master is the controlling end, and slave the end a
// program uses as its terminal.
func Open(cols, rows int) (master, slave *os.File, err error) {
	master, err = os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("pty: open the terminal multiplexer device: %w", err)
	}
	name, err := unlock(master)
	if err != nil {
		master.Close()
		return nil, nil, err
	}
	slave, err = os.OpenFile(name, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		master.Close()
		return nil, nil, fmt.Errorf("pty: open %s: %w", name, err)
	}
	// macOS answers ENOTTY to a size set on the controlling end until the other end has been opened.
	if err := SetSize(master, cols, rows); err != nil {
		slave.Close()
		master.Close()
		return nil, nil, err
	}
	return master, slave, nil
}

// SetSize tells a terminal its size. A darwin host takes the request on the other end.
func SetSize(master *os.File, cols, rows int) error {
	ws := &unix.Winsize{Col: uint16(max(1, cols)), Row: uint16(max(1, rows))}
	var err error
	if onDarwin {
		err = setSizeDarwin(master, ws)
	} else {
		err = control(master, func(fd int) error { return unix.IoctlSetWinsize(fd, unix.TIOCSWINSZ, ws) })
	}
	if err != nil {
		return fmt.Errorf("pty: set the terminal size: %w", err)
	}
	return nil
}

// control runs fn on the descriptor without taking the file out of the poller.
func control(f *os.File, fn func(fd int) error) error {
	conn, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var inner error
	if err := conn.Control(func(fd uintptr) { inner = fn(int(fd)) }); err != nil {
		return err
	}
	return inner
}

func (p *Pty) wait() {
	err := p.cmd.Wait()
	var exit *exec.ExitError
	switch {
	case err == nil:
		p.status = 0
	case errors.As(err, &exit):
		p.status = exit.ExitCode()
	default:
		p.status = -1
	}
	close(p.done)
}

// Read reads the child's output. It reports io.EOF once the child has closed the terminal.
func (p *Pty) Read(b []byte) (int, error) {
	n, err := p.master.Read(b)
	if err != nil && n == 0 {
		// The kernel answers a read with an I/O error once no process holds the other end.
		return 0, io.EOF
	}
	return n, nil
}

// Write sends input to the child.
func (p *Pty) Write(b []byte) (int, error) { return p.master.Write(b) }

// Resize tells the terminal its size, and the kernel tells the child.
func (p *Pty) Resize(cols, rows int) error { return SetSize(p.master, cols, rows) }

// Pid is the child's process id.
func (p *Pty) Pid() int { return p.cmd.Process.Pid }

// Done is closed once the child has exited.
func (p *Pty) Done() <-chan struct{} { return p.done }

// ExitCode is the child's exit status, valid once Done is closed. A child a signal ended reports a negative status.
func (p *Pty) ExitCode() int { return p.status }

// Close hangs the terminal up, which ends a child that still runs, and releases the descriptor.
func (p *Pty) Close() error {
	var err error
	p.closeOnce.Do(func() {
		// Closing the controlling end sends the hangup to the session. A child that ignores it is killed.
		err = p.master.Close()
		select {
		case <-p.done:
		default:
			_ = p.cmd.Process.Signal(syscall.SIGHUP)
		}
	})
	return err
}

// Kill ends the child without waiting for it to honour a hangup.
func (p *Pty) Kill() { _ = p.cmd.Process.Kill() }

// Foreground is the process group now in the foreground of the terminal: the shell at a prompt, or the job it runs.
func (p *Pty) Foreground() (int, error) {
	if onDarwin {
		return foregroundDarwin(p.master)
	}
	var pgrp int
	err := control(p.master, func(fd int) error {
		var err error
		pgrp, err = unix.IoctlGetInt(fd, unix.TIOCGPGRP)
		return err
	})
	return pgrp, err
}

// Process describes one process of the host.
type Process struct {
	// Name is the short command name.
	Name string
	// Dir is the working directory.
	Dir string
}

// Describe reads a process's name and working directory. A process that is gone reports an error.
func Describe(pid int) (Process, error) {
	if onDarwin {
		return describeDarwin(pid)
	}
	return describeLinux(pid)
}

// describeLinux reads the proc filesystem.
func describeLinux(pid int) (Process, error) {
	root := "/proc/" + strconv.Itoa(pid)
	name, err := os.ReadFile(root + "/comm")
	if err != nil {
		return Process{}, err
	}
	dir, err := os.Readlink(root + "/cwd")
	if err != nil {
		return Process{}, err
	}
	return Process{Name: strings.TrimSpace(string(name)), Dir: dir}, nil
}
