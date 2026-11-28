package pty

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// drain reads the terminal until the child closes it.
func drain(t *testing.T, p *Pty) string {
	t.Helper()
	var out strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := p.Read(buf)
		out.Write(buf[:n])
		if err != nil {
			require.ErrorIs(t, err, io.EOF)
			return out.String()
		}
	}
}

func shell(script string) Command {
	return Command{Path: "/bin/sh", Args: []string{"-c", script}, Env: os.Environ(), Cols: 91, Rows: 17}
}

func TestStartRunsTheCommandOnATerminal(t *testing.T) {
	p, err := Start(shell("test -t 0 && test -t 1 && stty size"))
	require.NoError(t, err)
	defer p.Close()

	out := drain(t, p)
	<-p.Done()
	assert.Equal(t, 0, p.ExitCode())
	assert.Contains(t, out, "17 91")
}

func TestExitCodeIsTheChildsStatus(t *testing.T) {
	p, err := Start(shell("exit 7"))
	require.NoError(t, err)
	defer p.Close()
	drain(t, p)
	<-p.Done()
	assert.Equal(t, 7, p.ExitCode())
}

func TestWriteReachesTheChildAndResizeReachesTheTerminal(t *testing.T) {
	p, err := Start(shell("read line; stty size; echo got:$line"))
	require.NoError(t, err)
	defer p.Close()

	require.NoError(t, p.Resize(120, 40))
	_, err = p.Write([]byte("hello\n"))
	require.NoError(t, err)

	out := drain(t, p)
	assert.Contains(t, out, "40 120")
	assert.Contains(t, out, "got:hello")
	assert.Positive(t, p.Pid())
}

func TestCloseEndsAChildThatStillRuns(t *testing.T) {
	p, err := Start(shell("sleep 600"))
	require.NoError(t, err)
	require.NoError(t, p.Close())
	select {
	case <-p.Done():
	case <-time.After(10 * time.Second):
		p.Kill()
		t.Fatal("the child outlived its terminal")
	}
	assert.NotEqual(t, 0, p.ExitCode())
	require.NoError(t, p.Close())
}

func TestKillEndsTheChild(t *testing.T) {
	p, err := Start(shell("trap '' HUP; sleep 600"))
	require.NoError(t, err)
	defer p.Close()
	p.Kill()
	select {
	case <-p.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the child survived a kill")
	}
	assert.Negative(t, p.ExitCode())
}

func TestStartReportsAProgramThatDoesNotExist(t *testing.T) {
	_, err := Start(Command{Path: "/nonexistent/program", Cols: 80, Rows: 24})
	require.ErrorContains(t, err, "/nonexistent/program")
}

func TestForegroundAndDescribeNameTheRunningJob(t *testing.T) {
	dir := t.TempDir()
	cmd := shell("echo ready; sleep 600")
	cmd.Dir = dir
	p, err := Start(cmd)
	require.NoError(t, err)
	defer p.Close()

	buf := make([]byte, 64)
	_, err = p.Read(buf)
	require.NoError(t, err)

	pgrp, err := p.Foreground()
	require.NoError(t, err)
	assert.Equal(t, p.Pid(), pgrp)

	proc, err := Describe(pgrp)
	require.NoError(t, err)
	assert.NotEmpty(t, proc.Name)
	// macOS keeps the temporary directory behind a symbolic link, and the kernel reports the real path.
	resolved, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	assert.Equal(t, resolved, proc.Dir)
}

func TestDescribeReportsAProcessThatIsGone(t *testing.T) {
	_, err := Describe(1 << 30)
	require.Error(t, err)
}
