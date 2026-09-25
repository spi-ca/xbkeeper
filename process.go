//go:build linux

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Stdout and stderr may be copied concurrently by os/exec.
type limitedWriter struct {
	mu   sync.Mutex
	w    io.Writer
	left int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := len(p)
	if l.left > 0 {
		part := p
		if len(part) > l.left {
			part = part[:l.left]
		}
		written, err := l.w.Write(part)
		l.left -= written
		if err != nil {
			return written, err
		}
	}
	return n, nil
}

type childExit struct{ code int }

func (e *childExit) Error() string { return "xtrabackup exited unsuccessfully" }

// A failed containment check must quarantine the stage and block subsequent runs.
type containmentUncertain struct{}

func (*containmentUncertain) Error() string {
	return "process group containment unverified; staging quarantined"
}

var inspectGroup = groupActive
var inspectLeader = leaderActive

func liveStat(b []byte, pgid int) (bool, error) {
	pos := bytes.LastIndexByte(b, ')')
	if pos < 0 {
		return false, errors.New("invalid proc stat")
	}
	fields := strings.Fields(string(b[pos+1:]))
	if len(fields) < 3 {
		return false, errors.New("invalid proc stat")
	}
	group, err := strconv.Atoi(fields[2])
	if err != nil {
		return false, err
	}
	return group == pgid && fields[0] != "Z" && fields[0] != "X", nil
}

// The direct child remains unreaped until group signaling and inspection end,
// preventing PGID reuse. Detached descendants are outside this contract.
func leaderActive(pgid int) (bool, error) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pgid) + "/stat")
	if err != nil {
		return false, err
	}
	return liveStat(b, pgid)
}

func groupActive(pgid int) (bool, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return false, err
		}
		active, err := liveStat(b, pgid)
		if err != nil {
			return false, err
		}
		if active {
			return true, nil
		}
	}
	return false, nil
}

// waitChild bounds waiting for inherited pipes after the leader has exited.
func waitChild(cmd *exec.Cmd) error {
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		return &containmentUncertain{}
	}
}

func runChild(ctx context.Context, bin string, args []string, output io.Writer) error {
	return runChildStreams(ctx, bin, args, output, output)
}

func runChildStreams(ctx context.Context, bin string, args []string, stdout, stderr io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cmd := exec.Command(bin, args...)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		return errors.New("cannot start xtrabackup")
	}
	pgid := cmd.Process.Pid
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	var termAt time.Time
	var killAt time.Time
	var inspectionFailed bool
	for {
		// Poll only the direct leader during ordinary execution. Once it exits,
		// scan the group to catch descendants that still hold the target open.
		leader, err := inspectLeader(pgid)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			inspectionFailed = true
		}
		stopping := ctx.Err() != nil || inspectionFailed
		if stopping && termAt.IsZero() {
			_ = syscall.Kill(-pgid, syscall.SIGTERM)
			termAt = time.Now()
		}
		if stopping && killAt.IsZero() && time.Since(termAt) >= 2*time.Second {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
			killAt = time.Now()
		}
		if stopping || !leader || errors.Is(err, os.ErrNotExist) {
			active, scanErr := inspectGroup(pgid)
			if scanErr == nil && !active {
				waitErr := waitChild(cmd)
				var uncertain *containmentUncertain
				if errors.As(waitErr, &uncertain) || errors.Is(waitErr, exec.ErrWaitDelay) {
					return &containmentUncertain{}
				}
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if inspectionFailed {
					return errors.New("xtrabackup inspection failed")
				}
				if waitErr != nil {
					var exit *exec.ExitError
					if errors.As(waitErr, &exit) {
						return &childExit{code: exit.ExitCode()}
					}
					return errors.New("xtrabackup exited unsuccessfully")
				}
				return nil
			}
			if scanErr != nil && termAt.IsZero() {
				inspectionFailed = true
				_ = syscall.Kill(-pgid, syscall.SIGTERM)
				termAt = time.Now()
			}
			if !termAt.IsZero() && time.Since(termAt) >= 4*time.Second {
				// Reap an exited direct leader if possible; never wait indefinitely
				// for a stuck child or inherited output pipe.
				if !leader {
					_ = waitChild(cmd)
				}
				return &containmentUncertain{}
			}
		}
		<-tick.C
	}
}
