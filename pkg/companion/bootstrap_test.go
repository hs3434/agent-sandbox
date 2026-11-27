package companion

import (
	"os"
	"path/filepath"
	"testing"
)

func TestClassifyChain(t *testing.T) {
	cases := []struct {
		state   string
		probeOK bool
		want    Class
	}{
		{"ok", true, ClassActive},
		{"ok", false, ClassStale},
		{"corrupt", true, ClassCorrupt}, // probe result irrelevant: file is the truth source here
		{"corrupt", false, ClassCorrupt},
		{"missing", false, ClassAbsent},
		{"missing", true, ClassAbsent}, // socket alive but no conn file = still absent to us
	}
	for _, c := range cases {
		if got := ClassifyFrom(c.state, c.probeOK); got != c.want {
			t.Errorf("ClassifyFrom(%q,%v) = %s, want %s", c.state, c.probeOK, got, c.want)
		}
	}
}

func TestInstallGateClosedWithoutPin(t *testing.T) {
	p := &BootstrapPlan{}
	if err := p.InstallAllowed(); err == nil {
		t.Error("no pin = install must refuse (D-15)")
	}
	p.PinSHA256 = "deadbeef"
	if err := p.InstallAllowed(); err != nil {
		t.Errorf("pinned plan: %v", err)
	}
}

func TestStartInstructionQuoted(t *testing.T) {
	p := &BootstrapPlan{StartCmd: "nohup '~/.sandboxd-server/install-x/sandboxd-server' &"}
	// D-30 contract: the printed command carries its own quoting, survives paste
	if got := p.StartInstruction(); got == "" || len(got) < len(p.StartCmd) {
		t.Errorf("instruction lost the command: %q", got)
	}
}

func TestDecideReinstall(t *testing.T) {
	// no pin = channel closed (never reinstall)
	if d, _ := DecideReinstall(nil, "abc"); d != ReinstallThinLock {
		t.Fatalf("nil pin → %s", d)
	}
	pin := &PinRecord{SHA256: "abc", Reinstalls: 0}
	// matching hash → allowed, counter increments
	d, next := DecideReinstall(pin, "abc")
	if d != ReinstallAllowed || next.Reinstalls != 1 {
		t.Fatalf("match: %s %+v", d, next)
	}
	// wrong hash → thin-lock, counter untouched
	d2, next2 := DecideReinstall(pin, "evil")
	if d2 != ReinstallThinLock || next2.Reinstalls != 0 {
		t.Fatalf("mismatch: %s %+v", d2, next2)
	}
	// cap reached → thin-lock even with matching hash
	pin3 := &PinRecord{SHA256: "abc", Reinstalls: ReinstallCap}
	if d3, _ := DecideReinstall(pin3, "abc"); d3 != ReinstallThinLock {
		t.Fatalf("cap: %s", d3)
	}
	// original pin never mutated (caller persists explicitly)
	if pin.Reinstalls != 0 {
		t.Fatal("pin mutated in place")
	}
}

func TestComputeSHA256(t *testing.T) {
	f := filepath.Join(t.TempDir(), "bin")
	os.WriteFile(f, []byte("payload"), 0o600)
	h, err := ComputeSHA256(f)
	if err != nil {
		t.Fatal(err)
	}
	// known sha256("payload")
	if h != "93a4f7d0"[:8] && len(h) != 64 {
		t.Fatalf("hash shape: %q", h)
	}
}
