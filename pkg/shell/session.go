package shell

import (
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Session is a persistent shell on the target: one long-lived process with
// stdin/stdout pipes. Commands are framed by unique markers (no PTY, no
// prompt scraping) — the drain goroutine watches for the END marker to know
// a command finished and to read its exit code.
//
// This ports the Python predecessor's shell-session design (ShellSession
// + marker framing); the interactive write_stdin half is deliberately NOT
// ported (AD-025: unknown-time stdin interaction is a REPL anti-pattern).
type Session struct {
	ID      string
	Env     string // target selector: env name or machine name
	Dialect Dialect

	write func(data []byte) error // feed the session's stdin
	close func()                  // tear the process down

	mu       sync.Mutex
	pending  string        // marker awaiting its END frame
	count    int           // sends so far (marker uniqueness)
	lines    []string      // accumulated output of the pending command
	exitCode int           // exit code from the last completed command
	done     chan struct{} // closed when the pending command's marker arrived
	closed   bool
	state    string // ready | terminated
}

// NewSession wires a session to transport-level pipes.
func NewSession(id, env string, d Dialect, stdin func([]byte) error, stdout io.Reader, teardown func()) *Session {
	s := &Session{
		ID:      id,
		Env:     env,
		Dialect: d,
		write:   stdin,
		close:   teardown,
		state:   "ready",
		done:    make(chan struct{}),
	}
	go s.drain(stdout)
	return s
}

var (
	markerStartRe = regexp.MustCompile(`__SB_START_[0-9a-f]+__\n?`)
	markerEndRe   = regexp.MustCompile(`__SB_END_([0-9a-f]+)__:(\d+)`)
	ansiRe        = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]`)
)

// drain reads the session's stdout forever, watching for END markers.
func (s *Session) drain(r io.Reader) {
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			s.consume(string(buf[:n]))
		}
		if err != nil {
			s.mu.Lock()
			s.state = "terminated"
			s.closed = true
			// a pending command ends with whatever we have (process died)
			if s.pending != "" {
				select {
				case <-s.done:
				default:
					close(s.done)
				}
			}
			s.mu.Unlock()
			return
		}
	}
}

// consume strips framing noise and updates pending state.
func (s *Session) consume(chunk string) {
	chunk = ansiRe.ReplaceAllString(chunk, "")
	s.mu.Lock()
	defer s.mu.Unlock()

	if m := markerEndRe.FindStringSubmatch(chunk); m != nil && m[1] == s.pending {
		// text before the marker on the same line belongs to the payload
		before := chunk[:strings.Index(chunk, "__SB_END_"+m[1]+"__")]
		s.lines = append(s.lines, splitKeep(before)...)
		fmt.Sscanf(m[2], "%d", &s.exitCode)
		s.pending = ""
		select {
		case <-s.done:
		default:
			close(s.done)
		}
		return
	}
	s.lines = append(s.lines, splitKeep(chunk)...)
}

// splitKeep splits a chunk into clean lines, keeping a trailing partial line
// for later chunks to complete.
func splitKeep(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	parts := strings.Split(s, "\n")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	var out []string
	for _, p := range parts {
		p = strings.TrimRight(p, "\r")
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Send runs one command inside the session and blocks until its END marker
// (or timeout). The session's state (cwd, exports, background jobs) persists
// across sends — that is the point of the session.
func (s *Session) Send(cmd string, timeout time.Duration) (output string, exitCode int, err error) {
	s.mu.Lock()
	if s.closed || s.state == "terminated" {
		s.mu.Unlock()
		return "", 130, fmt.Errorf("session %s terminated", s.ID)
	}
	if s.pending != "" {
		s.mu.Unlock()
		return "", 0, fmt.Errorf("session %s busy: another command is running", s.ID)
	}
	marker := fmt.Sprintf("%d%x", time.Now().UnixNano(), s.count)
	s.count++
	s.pending = marker
	s.lines = nil
	s.exitCode = 0
	s.done = make(chan struct{})
	s.mu.Unlock()

	frame := s.Dialect.FrameCommand(cmd, marker)
	if err := s.write([]byte(frame + "\n")); err != nil {
		return "", 130, fmt.Errorf("session write: %w", err)
	}

	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	select {
	case <-s.done:
	case <-time.After(timeout):
		// expiry is a fact, not a hang: report what we have and free the
		// session for the next command (the marker may still land later —
		// its text then shows up in Read())
		s.mu.Lock()
		s.pending = ""
		out := strings.Join(s.lines, "\n")
		s.lines = nil
		s.mu.Unlock()
		return out, 124, fmt.Errorf("session %s: command timed out after %s (session stays alive; output so far kept)", s.ID, timeout)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	out := strings.Join(s.lines, "\n")
	s.lines = nil
	return out, s.exitCode, nil
}

// Read returns output accumulated without a pending command (e.g. from
// background jobs started inside the session).
func (s *Session) Read() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := strings.Join(s.lines, "\n")
	s.lines = nil
	return out
}

// Health reports the session's liveness.
func (s *Session) Health() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == "" {
		return "ready"
	}
	return s.state
}

// Close tears the session down (idempotent).
func (s *Session) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.state = "terminated"
	s.mu.Unlock()
	if s.close != nil {
		s.close()
	}
}

// Busy reports whether a command is in flight (registry health display).
func (s *Session) Busy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pending != ""
}

