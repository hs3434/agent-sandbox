// sandboxd-server is the companion daemon (T5, D-01): the ssh axis's
// resident protocol peer. It serves a unix socket with JSON-lines protocol
// (pkg/contract vocabulary, framing per D-25), admits clients by token
// fail-closed (D-16), refuses root (D-17), and publishes its discovery file
// atomically (D-18). Composition root: exec/file supervisors land in T8/T9.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sync"

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
		_ = os.Remove(filepath.Join(companion.Dir(*home), "conn.json"))
		os.Exit(0)
	}()

	log.Printf("sandboxd-server ready: proto=%s owner=%s uid=%d socket=%s",
		companion.ProtoVersion, *owner, uid, socket)
	for {
		c, err := ln.Accept()
		if err != nil {
			return // listener closed by shutdown path
		}
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

	// events for this connection (opt-in via "events" method)
	evCh := make(chan map[string]any, 64)
	pushMu.Lock()
	pushers[c] = evCh
	pushMu.Unlock()
	defer func() {
		pushMu.Lock()
		delete(pushers, c)
		pushMu.Unlock()
	}()
	go func() {
		for ev := range evCh {
			if protocol.WriteMessage(c, ev) != nil {
				return
			}
		}
	}()

	for {
		msg, err := protocol.ReadMessage(br)
		if err != nil {
			return
		}
		resp := dispatch(msg)
		if err := protocol.WriteMessage(c, resp); err != nil {
			return
		}
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
		// fork the setsid'd group here (the daemon owns the children);
		// the supervisor records + reaps
		pc := exec.Command("/bin/sh", "-c", cmd)
		pc.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := pc.Start(); err != nil {
			resp.Error = &protocol.ProtoError{Code: "fork_failed", Message: err.Error()}
			break
		}
		pgid := pc.Process.Pid
		go func() { _ = pc.Wait() }()
		h, existed, err := sup.ExecAsync(reqID, cmd, ownerFromHandshake(msg), pgid)
		if err != nil {
			resp.Error = &protocol.ProtoError{Code: "cap_refused", Message: err.Error()}
			break
		}
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
		sup.MarkKilledByUs(h)
		if err := sup.Kill(h); err != nil {
			resp.Error = &protocol.ProtoError{Code: "kill_failed", Message: err.Error()}
			break
		}
		resp.Result = map[string]any{"handle_id": h, "killed": true}
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
