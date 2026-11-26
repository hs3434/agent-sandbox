// bootstrap is the sandboxd-side deploy path for the companion (T5,
// D-13/D-15/D-18): copy the pinned binary over ssh, print (never auto-run)
// the start command for the human gate, then classify the remote state via
// probe→adopt→thin. The heaviest operation (install) is never a fallback.
package companion

import "fmt"

// BootstrapPlan is what one deploy step needs: everything is data, execution
// stays with the ssh transport (pkg/remote) — this package never dials.
type BootstrapPlan struct {
	BinaryPath  string // local linux/amd64 sandboxd-server binary
	RemotePath  string // destination path on the target machine
	OwnerID     string // platform owner (D-18 isolation)
	PinSHA256   string // D-15 pin — empty means install channel is CLOSED
	Approver    string // who pinned (audit row D-27)
	AllowRoot   bool   // D-17 flag passthrough (test envs)
	StartCmd    string // the exact command the human runs (D-30 quoted)
	ConfirmMode string // auto (tests) | manual (production default, D-13)
}

// Classify is the degrade-chain verdict (D-18) after probing a remote home.
type Class string

const (
	// ClassActive: conn.json ok + socket answers ping → adopt, no install.
	ClassActive Class = "active"
	// ClassStale: conn file exists but socket dead → stale daemon; recover
	// by restarting (human-confirmed start cmd), not reinstalling.
	ClassStale Class = "stale"
	// ClassCorrupt: conn.json unparseable → thin until human restarts with
	// a clean start (the file gets rewritten by the new daemon on start).
	ClassCorrupt Class = "corrupt"
	// ClassAbsent: nothing ever ran → install requires a pinned sha256.
	ClassAbsent Class = "absent"
)

// ClassifyFrom describes the remote state from lenient discovery facts.
// probeOK is the socket round-trip verdict (caller probes via ssh exec).
func ClassifyFrom(readState string, probeOK bool) Class {
	switch readState {
	case "ok":
		if probeOK {
			return ClassActive
		}
		return ClassStale
	case "corrupt":
		return ClassCorrupt
	default: // missing
		return ClassAbsent
	}
}

// InstallAllowed is the D-15 gate: auto-install ONLY against the pinned
// hash; no pin = the channel is closed (D-13's code form).
func (p *BootstrapPlan) InstallAllowed() error {
	if p.PinSHA256 == "" {
		return fmt.Errorf("no sha256 pin recorded: unattended install channel is closed " +
			"(have a human approve a binary hash first, D-15)")
	}
	return nil
}

// StartInstruction renders the human-facing start command via the quoting
// contract (D-30): what we print must survive a paste.
func (p *BootstrapPlan) StartInstruction() string {
	return "run on the target machine: " + p.StartCmd
}
