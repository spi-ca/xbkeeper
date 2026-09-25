//go:build linux

package main

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"sync"
)

const maxVerboseLine = 4096
const maxVerboseOutput = 1 << 20

// commandTail keeps only the last maxLog bytes on disk, outside the child target.
// os/exec may copy stdout and stderr concurrently.
type commandTail struct {
	mu        sync.Mutex
	file      *os.File
	pos, size int
}

func (t *commandTail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := len(p)
	if len(p) >= maxLog {
		p = p[len(p)-maxLog:]
		if _, err := t.file.WriteAt(p, 0); err != nil {
			return 0, err
		}
		t.pos, t.size = 0, maxLog
		return n, nil
	}
	for len(p) > 0 {
		part := p
		if len(part) > maxLog-t.pos {
			part = part[:maxLog-t.pos]
		}
		if _, err := t.file.WriteAt(part, int64(t.pos)); err != nil {
			return 0, err
		}
		t.pos = (t.pos + len(part)) % maxLog
		if t.size+len(part) < maxLog {
			t.size += len(part)
		} else {
			t.size = maxLog
		}
		p = p[len(part):]
	}
	return n, nil
}

func (t *commandTail) Bytes() ([]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	b := make([]byte, t.size)
	start := 0
	if t.size == maxLog {
		start = t.pos
	}
	first := t.size
	if first > maxLog-start {
		first = maxLog - start
	}
	if _, err := t.file.ReadAt(b[:first], int64(start)); err != nil {
		return nil, err
	}
	if first < len(b) {
		if _, err := t.file.ReadAt(b[first:], 0); err != nil {
			return nil, err
		}
	}
	return b, nil
}

// A pair of bounded line writers shares a single emitted-byte budget per backup.
// Excess data is drained but never logged; no goroutine is created per line.
type verboseBudget struct {
	mu        sync.Mutex
	remaining int
	logger    *slog.Logger
}
type lineOutput struct {
	budget        *verboseBudget
	phase, stream string
	buf           []byte
	truncated     bool
}

func (l *lineOutput) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		part := p
		if i >= 0 {
			part = p[:i]
		}
		if len(part) > maxVerboseLine-len(l.buf) {
			l.buf = append(l.buf, part[:maxVerboseLine-len(l.buf)]...)
			l.truncated = true
		} else {
			l.buf = append(l.buf, part...)
		}
		if i < 0 {
			break
		}
		l.flush()
		p = p[i+1:]
	}
	return n, nil
}
func (l *lineOutput) flush() {
	if len(l.buf) == 0 && !l.truncated {
		return
	}
	b := l.budget
	b.mu.Lock()
	// Reserve space for slog's metadata/escaping too, not just the raw line.
	const eventOverhead = 256
	if b.remaining > eventOverhead {
		content := l.buf
		if len(content) > (b.remaining-eventOverhead)/4 {
			content = content[:(b.remaining-eventOverhead)/4]
		}
		b.remaining -= 4*len(content) + eventOverhead
		b.logger.Info("child output", "phase", l.phase, "stream", l.stream, "line", string(content), "truncated", l.truncated || len(content) < len(l.buf))
	}
	b.mu.Unlock()
	l.buf = l.buf[:0]
	l.truncated = false
}
func (l *lineOutput) close() { l.flush() }

func childOutputs(log io.Writer, budget *verboseBudget, phase string) (io.Writer, io.Writer, func()) {
	if budget == nil {
		return log, log, func() {}
	}
	stdout := &lineOutput{budget: budget, phase: phase, stream: "stdout"}
	stderr := &lineOutput{budget: budget, phase: phase, stream: "stderr"}
	return io.MultiWriter(log, stdout), io.MultiWriter(log, stderr), func() { stdout.close(); stderr.close() }
}
