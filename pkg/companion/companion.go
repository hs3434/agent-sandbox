// Package companion implements the sandboxd-server lifecycle contract
// (T5, D-01/D-02/D-13/D-15/D-16/D-17/D-18/D-27): discovery file I/O
// (atomic conn.json, lenient reads), the state machine probe→adopt→thin,
// and the trust-pinned bootstrap facts. The server binary (cmd/sandboxd-
// server) is the composition root; this package owns the *semantics*.
package companion

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ProtoVersion mirrors pkg/protocol.ProtoVersion (contract vocabulary is
// shared; the companion does not import the protocol codec — its framing
// lives server-side in main.go).
const ProtoVersion = "1.0.0"

// DirName is the discovery directory under the remote user's home.
const DirName = ".sandboxd-server"

// BindAddr is the D-31 transport's only legal bind address: loopback. The
// listener is never reachable off-host; clients ride ssh direct-tcpip, so
// the bind address itself is the first security gate (token is the second).
const BindAddr = "127.0.0.1"

// Conn is the discovery file contract (~/.sandboxd-server/conn.json): where
// the listener lives and which token admits clients (D-16, D-31). Transport
// is a loopback TCP listener: BindAddr is always 127.0.0.1 (never exposed,
// firewall-invisible) and Port is kernel-assigned at boot (no mgmt). Socket
// is legacy (D-30 unix-socket era); readers prefer Port, older binaries
// keep parsing it so a stale conn.json never reads as corrupt.
// Written atomically (tmp+rename, 0600 dir/file) — readers never see a torn
// file (D-18).
type Conn struct {
	Token        string `json:"token"`
	Socket       string `json:"socket,omitempty"`
	BindAddr     string `json:"bind_addr,omitempty"`
	Port         int    `json:"port,omitempty"`
	ProtoVersion string `json:"proto_version"`
	OwnerID      string `json:"owner_id"`
	Pid          int    `json:"pid"`
}

// Endpoint is the transport address clients dial (D-31): "127.0.0.1:port"
// for TCP conns, the unix socket path as legacy fallback.
func (c *Conn) Endpoint() string {
	if c.Port > 0 {
		if c.BindAddr == "" {
			c.BindAddr = "127.0.0.1"
		}
		return fmt.Sprintf("%s:%d", c.BindAddr, c.Port)
	}
	return c.Socket
}

// Home returns the discovery directory for a user home (~ expansion is the
// caller's business — ssh paths arrive as ~/, local paths resolve here).
func Dir(home string) string { return filepath.Join(home, DirName) }

// SocketPath is the owner-namespaced unix socket inside the discovery dir
// (D-18: two owners on one machine get independent daemons — the socket
// node itself is part of the isolation, not just the install dir).
func SocketPath(home, ownerID string) string {
	if strings.TrimSpace(ownerID) == "" {
		ownerID = "default"
	}
	return filepath.Join(Dir(home), "sandboxd-"+ownerID+".sock")
}

// ConnPath is the discovery file itself — owner-namespaced like the socket
// (D-18): two owners on one home get independent daemon records; sharing the
// file would be last-writer-wins, the corruption mode this card rejects.
// ConnFileName is the owner-namespaced discovery file's basename — the ssh
// bootstrap path is `cat ~/.sandboxd-server/<ConnFileName>` (tilde-expansion
// is the transport's job; DirName is part of the tilde literal there).
func ConnFileName(ownerID string) string {
	return filepath.Base(ConnPath("/x", ownerID))
}

func ConnPath(home, ownerID string) string {
	if strings.TrimSpace(ownerID) == "" {
		ownerID = "default"
	}
	return filepath.Join(Dir(home), "conn-"+ownerID+".json")
}

// NewToken generates the socket admission secret (128-bit, hex).
func NewToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// WriteConn atomically persists the discovery file (D-18): write tmp in the
// SAME directory (rename must not cross filesystems), fsync, rename, then
// re-verify the mode bit. 0700 dir + 0600 file are invariants, not defaults.
func WriteConn(home string, c *Conn) error {
	dir := Dir(home)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("discovery dir: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil { // MkdirAll tolerates pre-existing loose dirs
		return fmt.Errorf("discovery dir mode: %w", err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "conn.json.tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("conn tmp write: %w", err)
	}
	if f, err := os.Open(tmp); err == nil {
		_ = f.Sync() // rename-after-fsync: no torn file on crash (D-18)
		_ = f.Close()
	}
	if err := os.Rename(tmp, ConnPath(home, c.OwnerID)); err != nil {
		return fmt.Errorf("conn rename: %w", err)
	}
	return os.Chmod(ConnPath(home, c.OwnerID), 0o600)
}

// ReadConnState is the lenient discovery read (D-18 degrade chain input):
// ok = valid conn file; corrupt = parse failure (adopt possible if the
// socket answers); missing = nothing ever ran here.
type ReadConnState struct {
	State string // ok | corrupt | missing
	Conn  *Conn
}

// ReadConn reads the discovery file leniently — corrupt never triggers an
// install; it only reports (the heaviest operation is never the fallback).
func ReadConn(home, ownerID string) ReadConnState {
	data, err := os.ReadFile(ConnPath(home, ownerID))
	if os.IsNotExist(err) {
		return ReadConnState{State: "missing"}
	}
	if err != nil {
		return ReadConnState{State: "corrupt"}
	}
	var c Conn
	if err := json.Unmarshal(data, &c); err != nil || c.Token == "" || c.Endpoint() == "" {
		return ReadConnState{State: "corrupt"}
	}
	return ReadConnState{State: "ok", Conn: &c}
}

// Platform reports this binary's os/arch (the handshake's matrix fields).
func Platform() (string, string) { return runtime.GOOS, runtime.GOARCH }

// Supported tells whether a companion can serve this platform (D-20 matrix,
// extended by D-32: linux/amd64 + linux/arm64 + windows/amd64 yes;
// everything else pins thin — honest, not a silent degradation).
func Supported(goos, goarch string) bool {
	if goos == "windows" {
		return goarch == "amd64"
	}
	if goos != "linux" {
		return false
	}
	return goarch == "amd64" || goarch == "arm64"
}

// FileExists is the plain existence probe (proc_windows shell discovery).
func FileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// RootGuard is the D-17 invariant: uid0 refuses to serve unless the operator
// explicitly flipped allow_root. Text warnings are not code — this is code.
func RootGuard(uid int, allowRoot bool) error {
	if uid == 0 && !allowRoot {
		return fmt.Errorf("refusing to run as root (uid 0): whole-machine exec daemon " +
			"as root is reachable by every agent; pass --allow_root only if you mean it")
	}
	return nil
}

// InstallDir resolves the owner-isolated install root (D-18): two owners on
// one machine never share a daemon — the directory carries the owner id.
func InstallDir(home, ownerID string) string {
	if strings.TrimSpace(ownerID) == "" {
		ownerID = "default"
	}
	return filepath.Join(Dir(home), "install-"+ownerID)
}

// PinRecord is the D-15/D-27 trust row: the sha256 we may auto-install and
// the human who approved it. Written at pin time to the audit ledger (the
// platform side keeps the fact; the companion never self-upgrades).
type PinRecord struct {
	SHA256     string `json:"sha256"`
	Approver   string `json:"approver"`
	PinnedAt   string `json:"pinned_at"`
	Reinstalls int    `json:"reinstalls"`
}

// PinPath locates the pin record inside an install dir.
func PinPath(installDir string) string { return filepath.Join(installDir, "pin.json") }

// WritePin persists the trust row (0600 — it authorizes a deploy channel).
func WritePin(installDir string, p *PinRecord) error {
	if err := os.MkdirAll(installDir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(PinPath(installDir), data, 0o600)
}

// ReadPin loads the trust row (missing = never pinned: auto-install closed).
func ReadPin(installDir string) (*PinRecord, error) {
	data, err := os.ReadFile(PinPath(installDir))
	if err != nil {
		return nil, err
	}
	var p PinRecord
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("pin record corrupt: %w", err)
	}
	return &p, nil
}

// ReinstallCap is the D-15 lock: after this many mismatch-reinstalls the
// machine locks to thin until a human re-pins. A flaky binary channel must
// not become an install loop.
const ReinstallCap = 2
