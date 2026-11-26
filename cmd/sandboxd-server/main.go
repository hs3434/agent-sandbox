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
	"syscall"

	"github.com/hs3434/agent-sandbox/pkg/companion"
	"github.com/hs3434/agent-sandbox/pkg/protocol"
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

// dispatch resolves one request (method surface grows per wave: T8 exec,
// T9 files). Unknown methods are typed errors, never silence.
func dispatch(msg *protocol.Message) *protocol.Response {
	resp := &protocol.Response{ID: msg.ID}
	switch msg.Method {
	case "ping":
		resp.Result = map[string]any{"ok": true, "proto_version": companion.ProtoVersion}
	case "shutdown":
		resp.Result = map[string]any{"ok": true}
		log.Printf("shutdown requested by client")
		go func() { _ = syscall.Kill(os.Getpid(), syscall.SIGTERM) }()
	default:
		resp.Error = &protocol.ProtoError{
			Code: "unknown_method",
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
