// describe_darwin.go holds describeDarwin.

package pty

import (
	"bufio"
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// describeDarwin asks ps and lsof. macOS has no proc filesystem, and the cosmo runtime reaches neither sysctl nor
// libproc.
func describeDarwin(pid int) (Process, error) {
	id := strconv.Itoa(pid)
	out, err := exec.Command("/bin/ps", "-o", "comm=", "-p", id).Output()
	if err != nil {
		return Process{}, fmt.Errorf("pty: ps -p %s: %w", id, err)
	}
	// ps gives the path of the executable, and a login shell's name starts with a dash.
	name := strings.TrimPrefix(filepath.Base(strings.TrimSpace(string(out))), "-")
	if name == "" || name == "." {
		return Process{}, fmt.Errorf("pty: ps -p %s: no process", id)
	}
	out, err = exec.Command("/usr/sbin/lsof", "-a", "-d", "cwd", "-p", id, "-Fn").Output()
	if err != nil {
		return Process{}, fmt.Errorf("pty: lsof -p %s: %w", id, err)
	}
	// -Fn writes one field per line, and the name field starts with n.
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		if dir, ok := strings.CutPrefix(sc.Text(), "n"); ok && dir != "" {
			return Process{Name: name, Dir: dir}, nil
		}
	}
	return Process{}, fmt.Errorf("pty: lsof -p %s: no working directory", id)
}
