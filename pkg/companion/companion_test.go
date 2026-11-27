package companion

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteConnAtomicAndModes(t *testing.T) {
	home := t.TempDir()
	c := &Conn{Token: "tok", Socket: SocketPath(home, "default"), ProtoVersion: ProtoVersion, OwnerID: "default", Pid: 42}
	if err := WriteConn(home, c); err != nil {
		t.Fatalf("write: %v", err)
	}
	st := ReadConn(home, "default")
	if st.State != "ok" || st.Conn == nil || st.Conn.Token != "tok" {
		t.Fatalf("read state = %+v", st)
	}
	fi, err := os.Stat(ConnPath(home, "default"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("conn.json perm = %o, want 600", fi.Mode().Perm())
	}
	di, _ := os.Stat(Dir(home))
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("dir perm = %o, want 700", di.Mode().Perm())
	}
	// no tmp leftovers (atomic rename, not in-place)
	ents, _ := os.ReadDir(Dir(home))
	for _, e := range ents {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("tmp file left behind: %s", e.Name())
		}
	}
	// owner isolation: a second owner gets its own conn file
	c2 := &Conn{Token: "tok2", Socket: SocketPath(home, "bob"), ProtoVersion: ProtoVersion, OwnerID: "bob", Pid: 43}
	if err := WriteConn(home, c2); err != nil {
		t.Fatalf("write bob: %v", err)
	}
	if _, err := os.Stat(ConnPath(home, "bob")); err != nil {
		t.Fatalf("bob conn missing: %v", err)
	}
	stA := ReadConn(home, "default")
	if stA.Conn == nil || stA.Conn.OwnerID != "default" {
		t.Fatalf("alice row clobbered: %+v", stA)
	}
}

func TestReadConnLenient(t *testing.T) {
	home := t.TempDir()
	if st := ReadConn(home, "default"); st.State != "missing" {
		t.Errorf("missing = %s", st.State)
	}
	if err := os.MkdirAll(Dir(home), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ConnPath(home, "default"), []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if st := ReadConn(home, "default"); st.State != "corrupt" {
		t.Errorf("corrupt = %s", st.State)
	}
	// valid json but empty token is corrupt too (fail-closed contract)
	if err := os.WriteFile(ConnPath(home, "default"), []byte(`{"socket":"/x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if st := ReadConn(home, "default"); st.State != "corrupt" {
		t.Errorf("empty-token = %s, want corrupt", st.State)
	}
}

func TestRootGuard(t *testing.T) {
	if err := RootGuard(0, false); err == nil {
		t.Error("uid0 without allow_root must refuse")
	}
	if err := RootGuard(0, true); err != nil {
		t.Errorf("uid0 with allow_root: %v", err)
	}
	if err := RootGuard(1000, false); err != nil {
		t.Errorf("non-root: %v", err)
	}
}

func TestOwnerIsolation(t *testing.T) {
	if InstallDir("/h", "") == InstallDir("/h", "") {
		t.Log("empty owner collapses to default — fine")
	}
	if InstallDir("/h", "alice") == InstallDir("/h", "bob") {
		t.Error("distinct owners must get distinct install dirs (D-18)")
	}
}

func TestPlatformMatrix(t *testing.T) {
	if !Supported("linux", "amd64") || !Supported("linux", "arm64") {
		t.Error("linux amd64/arm64 must be supported")
	}
	if !Supported("windows", "amd64") {
		t.Error("windows amd64 must be supported (D-32 Job Object model landed)")
	}
	if Supported("windows", "arm64") || Supported("darwin", "arm64") {
		t.Error("unmatrixed platforms must pin thin (D-20, named not silent)")
	}
}
