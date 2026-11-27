// sandboxd-server is the companion daemon (T5, D-01): the ssh axis's
// resident protocol peer. It serves a unix socket with JSON-lines protocol
// (pkg/contract vocabulary, framing per D-25), admits clients by token
// fail-closed (D-16), refuses root (D-17), and publishes its discovery file
// atomically (D-18). Composition root: exec/file supervisors land in T8/T9.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"time"

	"os/exec"
	"strings"
	"syscall"

	"github.com/hs3434/agent-sandbox/pkg/companion"
	"github.com/hs3434/agent-sandbox/pkg/protocol"
)

// serverState is the per-process wiring (supervisor + push channels).
var (
	sup     *companion.Supervisor
	pushMu  sync.Mutex
	pushers = map[net.Conn]chan map[string]any{} // clients wanting events
)

func main() {
	home := flag.String("home", "", "target user home (default: $HOME)")
	owner := flag.String("owner_id", "default", "owner id — isolates daemons per platform owner (D-18)")
	allowRoot := flag.Bool("allow_root", false, "permit running as uid0 (D-17: humans only)")
	ttlSecs := flag.Int("ttl", 0, "idle-exit seconds: retire when no connections AND no running handles (0 = never)")
	flag.Parse()

	log.SetFlags(log.Ltime)

	if *home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			fatal("home: %v", err)
		}
		*home = h
	}

	// D-17 root guard: refuse before touching the filesystem
	uid := os.Getuid()
	if err := companion.RootGuard(uid, *allowRoot); err != nil {
		fatal("%v", err)
	}

	// D-20 matrix: unsupported platform pins thin — say so and exit 0 (the
	// absence IS the thin pin; probing learns it from this exit).
	if goos, goarch := companion.Platform(); !companion.Supported(goos, goarch) {
		log.Printf("platform %s/%s not supported — machine stays thin (managed by operator)", goos, goarch)
		os.Exit(0)
	}

	token, err := companion.NewToken()
	if err != nil {
		fatal("token: %v", err)
	}
	// discovery dir before the socket node (0700 — D-16's first gate)
	if err := os.MkdirAll(companion.Dir(*home), 0o700); err != nil {
		fatal("discovery dir: %v", err)
	}
	socket := companion.SocketPath(*home, *owner)
	_ = os.Remove(socket) // stale socket from a crash is ours to clean

	ln, err := net.Listen("unix", socket)
	if err != nil {
		fatal("listen %s: %v", socket, err)
	}
	// socket must be usable by the owning user only (D-16: 0700 dir is the
	// first gate; the socket node itself is the second)
	if err := os.Chmod(socket, 0o600); err != nil {
		fatal("socket mode: %v", err)
	}

	conn := &companion.Conn{
		Token:        token,
		Socket:       socket,
		ProtoVersion: companion.ProtoVersion,
		OwnerID:      *owner,
		Pid:          os.Getpid(),
	}
	if err := companion.WriteConn(*home, conn); err != nil {
		fatal("discovery write: %v", err)
	}

	// supervisor: disk-backed table + reaper loop (T8); events piggyback
	// the death callback (no extra goroutine per handle — D-06 shape)
	tableDir := filepath.Join(companion.Dir(*home), "procs")
	var errSup error
	sup, errSup = companion.NewSupervisor(tableDir)
	if errSup != nil {
		fatal("supervisor: %v", errSup)
	}
	sup.Start()
	defer sup.Stop()
	// TTL gate (F8): no connections + empty process table past --ttl →
	// graceful self-retirement (vscode-server lifecycle symmetry, D-02).
	// A running handle vetoes; zero-conf machines don't hold a daemon forever.
	if *ttlSecs > 0 {
		sup.SetTTL(time.Duration(*ttlSecs)*time.Second, func(why string) {
			log.Printf("ttl: retiring daemon (%s)", why)
			_ = os.Remove(companion.ConnPath(*home, *owner))
			_ = os.Remove(socket)
			os.Exit(0)
		})
	}
	sup.OnDead(func(handleID string, terminal string) {
		pushEvent(map[string]any{
			"event": "process.exited", "handle_id": handleID, "terminal": terminal,
		})
	})

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-stop
		log.Printf("shutdown: closing socket + discovery cleanup")
		ln.Close()
		_ = os.Remove(socket)
		_ = os.Remove(companion.ConnPath(*home, *owner))
		os.Exit(0)
	}()

	log.Printf("sandboxd-server ready: proto=%s owner=%s uid=%d socket=%s",
		companion.ProtoVersion, *owner, uid, socket)
	for {
		c, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return // listener closed by shutdown path
			}
			log.Printf("accept: %v (continuing)", err)
			continue
		}
		sup.NoteConn()
		go serveConn(c, token)
	}
}

// serveConn runs one client connection: handshake first line fail-closed
// (D-16), then JSON-lines request loop (D-25 framing is protocol-owned).
func serveConn(c net.Conn, token string) {
	defer c.Close()
	br := bufio.NewReaderSize(c, 64*1024)
	h, err := protocol.ReadHandshake(br)
	if err != nil {
		return // malformed/unreadable handshake: drop silently (quiet posture, D-27)
	}
	if h.Token != token {
		// auth failure gets ONE explicit line then the door closes
		_ = protocol.WriteMessage(c, map[string]any{"error": "auth rejected"})
		return
	}
	if h.ProtoVersion != companion.ProtoVersion {
		_ = protocol.WriteMessage(c, map[string]any{
			"error": "proto mismatch", "want": companion.ProtoVersion, "got": h.ProtoVersion})
		return
	}
	if err := json.NewEncoder(c).Encode(map[string]any{
		"ok": true, "proto_version": companion.ProtoVersion,
		"os": mustOS(), "arch": mustArch(), "owner_id": h.OwnerID,
	}); err != nil {
		return
	}

	// per-conn SINGLE writer: responses and event pushes serialize through
	// one queue — concurrent WriteMessage on one net.Conn interleaves lines
	// and desyncs the client's reader (the id-mismatch drop we chased).
	outCh := make(chan any, 128)
	done := make(chan struct{})
	defer close(done)
	go func() {
		for v := range outCh {
			if protocol.WriteMessage(c, v) != nil {
				return
			}
		}
	}()

	// events for this connection (opt-in via "events" method)
	evCh := make(chan map[string]any, 64)
	pushMu.Lock()
	pushers[c] = evCh
	pushMu.Unlock()
	defer func() {
		pushMu.Lock()
		delete(pushers, c)
		pushMu.Unlock()
		close(evCh) // M3: release the writer goroutine (one leak per conn otherwise)
	}()
	go func() {
		for ev := range evCh {
			select {
			case outCh <- ev:
			case <-done:
				return
			}
		}
	}()

	for {
		msg, err := protocol.ReadMessage(br)
		if err != nil {
			return
		}
		sup.NoteConn()
		resp := dispatch(msg)
		if _, ok := pushedMethods[msg.Method]; !ok {
			if err := writeOnly(outCh, done, resp); err != nil {
				return
			}
			continue
		}
		// streaming methods (events): the response acks, pushes flow after
		if err := writeOnly(outCh, done, resp); err != nil {
			return
		}
	}
}

// pushedMethods opt a connection into the event fan-out (payloads arrive
// WITHOUT an id — clients must expect interleaved-by-queue lines).
var pushedMethods = map[string]bool{"events": true}

func writeOnly(outCh chan any, done chan struct{}, v any) error {
	select {
	case outCh <- v:
		return nil
	case <-done:
		return fmt.Errorf("conn closing")
	}
}

// pushEvent fans one event out to every connected client (sandboxd only).
func pushEvent(ev map[string]any) {
	pushMu.Lock()
	defer pushMu.Unlock()
	for _, ch := range pushers {
		select {
		case ch <- ev:
		default: // drop on a slow client — events are advisory, requests are truth
		}
	}
}

// shQuote POSIX single-quote for wrapper paths (D-30 quoting contract).
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ownerFromHandshake extracts the declaring machine (best effort — request
// params carry it; the handshake's owner_id is the fallback).
func ownerFromHandshake(msg *protocol.Message) string {
	if m, ok := msg.Params["owner_machine"].(string); ok && m != "" {
		return m
	}
	return "default"
}

// dispatch resolves one request (method surface grows per wave: T8 exec,
// T9 files). Unknown methods are typed errors, never silence.
func dispatch(msg *protocol.Message) *protocol.Response {
	resp := &protocol.Response{ID: msg.ID}
	switch msg.Method {
	case "ping":
		resp.Result = map[string]any{"ok": true, "proto_version": companion.ProtoVersion}
	case "events":
		resp.Result = map[string]any{"ok": true, "note": "events will stream on this connection"}
	case "exec_async":
		cmd, _ := msg.Params["cmd"].(string)
		reqID, _ := msg.Params["request_id"].(string)
		if strings.TrimSpace(cmd) == "" {
			resp.Error = &protocol.ProtoError{Code: "bad_request", Message: "cmd required"}
			break
		}
		// reserve the row FIRST (cap + idempotency gates see the allocation);
		// a retry (existed=true) never forks twice — same handle, same pgid
		h, existed, err := sup.ExecAsync(reqID, cmd, ownerFromHandshake(msg), 0)
		if err != nil {
			resp.Error = &protocol.ProtoError{Code: "cap_refused", Message: err.Error()}
			break
		}
		if existed {
			if p, ok := sup.Wait(h); ok {
				resp.Result = map[string]any{"handle_id": h, "existed": true, "pgid": p.PGID}
			} else {
				resp.Result = map[string]any{"handle_id": h, "existed": true, "pgid": 0}
			}
			break
		}
		// fork the setsid'd group here (the daemon owns the children);
		// the supervisor records + reaps. Exit-code wrapper (D-05, docker
		// twin): the group leader's shell records its own terminal state to
		// a per-handle file — the reaper reads truth the child wrote, so a
		// natural death is exited(N), not exit_unknown.
		exitPath := sup.ExitFilePath(h)
		wrapped := fmt.Sprintf(`%s
__sb_code=$?
if [ $__sb_code -gt 128 ]; then echo "sig:$((__sb_code-128))" > %s; else echo "$__sb_code" > %s; fi`, cmd, shQuote(exitPath), shQuote(exitPath))
		pc := exec.Command("/bin/sh", "-c", wrapped)
		pc.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := pc.Start(); err != nil {
			sup.Remove(h) // row without a process is a lie — roll back
			resp.Error = &protocol.ProtoError{Code: "fork_failed", Message: err.Error()}
			break
		}
		pgid := pc.Process.Pid
		sup.SetPGID(h, pgid)
		go func() { _ = pc.Wait() }()
		resp.Result = map[string]any{"handle_id": h, "existed": existed, "pgid": pgid}
	case "wait":
		h, _ := msg.Params["handle_id"].(string)
		p, ok := sup.Wait(h)
		if !ok {
			resp.Error = &protocol.ProtoError{Code: "unknown_handle", Message: h}
			break
		}
		resp.Result = map[string]any{"handle_id": h, "state": p.State, "cmd": p.Cmd}
	case "kill":
		h, _ := msg.Params["handle_id"].(string)
		pgidF, hasPgid := msg.Params["pgid"].(float64)
		if h == "" && hasPgid {
			// pgid-addressed kill (cross-axis vocabulary)
			if err := sup.KillByPgid(int(pgidF)); err != nil {
				resp.Error = &protocol.ProtoError{Code: "kill_failed", Message: err.Error()}
				break
			}
			resp.Result = map[string]any{"pgid": int(pgidF), "killed": true}
			break
		}
		if err := sup.Kill(h); err != nil {
			resp.Error = &protocol.ProtoError{Code: "kill_failed", Message: err.Error()}
			break
		}
		sup.MarkKilledByUs(h) // after success: a natural death racing the kill stays honest
		resp.Result = map[string]any{"handle_id": h, "killed": true}
	case "file.read":
		path, _ := msg.Params["path"].(string)
		b64, durable, err := companion.FileRead(path)
		if err != nil {
			resp.Error = &protocol.ProtoError{Code: "file_error", Message: err.Error()}
			break
		}
		log.Printf("file.read %s durable=%t", path, durable) // trust row (D-27): ops visible, content never logged
		resp.Result = map[string]any{"path": path, "b64": b64, "durable": durable}
	case "file.write":
		path, _ := msg.Params["path"].(string)
		content, _ := msg.Params["b64"].(string)
		durable, err := companion.FileWrite(path, content)
		if err != nil {
			resp.Error = &protocol.ProtoError{Code: "file_error", Message: err.Error()}
			break
		}
		log.Printf("file.write %s durable=%t", path, durable)
		resp.Result = map[string]any{"path": path, "durable": durable, "written": true}
	case "file.patch":
		path, _ := msg.Params["path"].(string)
		oldS, _ := msg.Params["old"].(string)
		newS, _ := msg.Params["new"].(string)
		all, _ := msg.Params["all"].(bool)
		n, err := companion.FilePatch(path, oldS, newS, all)
		if err != nil {
			resp.Error = &protocol.ProtoError{Code: "file_error", Message: err.Error()}
			break
		}
		log.Printf("file.patch %s n=%d", path, n)
		resp.Result = map[string]any{"path": path, "replacements": n, "durable": true}
	case "file.search":
		root, _ := msg.Params["root"].(string)
		pattern, _ := msg.Params["pattern"].(string)
		fileGlob, _ := msg.Params["glob"].(string)
		limitF, _ := msg.Params["limit"].(float64)
		out, diag, err := companion.FileSearch(root, pattern, fileGlob, int(limitF))
		if err != nil && out == "" {
			resp.Error = &protocol.ProtoError{Code: "search_error", Message: err.Error()}
			break
		}
		resp.Result = map[string]any{"matches": out, "diagnostics": diag, "engine": "target-local"}
	case "sessions.list":
		rows := []map[string]any{}
		for _, p := range sup.List() {
			rows = append(rows, map[string]any{
				"handle_id": p.HandleID, "state": p.State, "cmd": p.Cmd,
				"pgid": p.PGID, "owner_machine": p.OwnerMach,
			})
		}
		resp.Result = map[string]any{"sessions": rows}
	case "shutdown":
		resp.Result = map[string]any{"ok": true}
		log.Printf("shutdown requested by client")
		go func() { _ = syscall.Kill(os.Getpid(), syscall.SIGTERM) }()
	default:
		resp.Error = &protocol.ProtoError{
			Code:    "unknown_method",
			Message: fmt.Sprintf("method %q not served (proto %s)", msg.Method, companion.ProtoVersion),
		}
	}
	return resp
}

func mustOS() string   { goos, _ := companion.Platform(); return goos }
func mustArch() string { _, a := companion.Platform(); return a }

func fatal(format string, args ...any) {
	log.Printf("FATAL: "+format, args...)
	os.Exit(1)
}
