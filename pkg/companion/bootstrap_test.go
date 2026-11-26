package companion

import "testing"

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
