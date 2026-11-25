package remote

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// sftpClient opens an SFTP session over a fresh ssh client connection.
// Thin mode note: we use direct ssh client connections for SFTP (the
// ControlMaster socket is OpenSSH-internal and not programmatically
// shareable); the master still accelerates exec, SFTP correctness is what
// the tier table promises (byte stream, no encoding issues).
func (b *Backend) sftpClient(ctx context.Context, machine string) (*sftp.Client, func(), error) {
	b.mu.Lock()
	st, ok := b.machines[machine]
	b.mu.Unlock()
	if !ok {
		return nil, nil, fmt.Errorf("machine %q not declared", machine)
	}
	if err := b.ensureAlive(st); err != nil {
		return nil, nil, err
	}
	cfg := &ssh.ClientConfig{
		User:            st.target.User,
		Auth:            sshAuth(st.target.Key),
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // thin-mode transition; companion adds TOFU
		Timeout:         DefaultConnectTO,
	}
	addr := fmt.Sprintf("%s:%d", st.target.Host, st.target.Port)
	conn, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("ssh dial %q: %w", machine, err)
	}
	cli, err := sftp.NewClient(conn)
	if err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("sftp client %q: %w", machine, err)
	}
	cleanup := func() {
		cli.Close()
		conn.Close()
	}
	return cli, cleanup, nil
}

func sshAuth(keyPath string) []ssh.AuthMethod {
	if keyPath == "" {
		keyPath = filepath.Join(os.Getenv("HOME"), ".ssh", "id_ed25519")
	}
	key, err := os.ReadFile(keyPath)
	if err != nil {
		return nil
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return nil
	}
	return []ssh.AuthMethod{ssh.PublicKeys(signer)}
}

func (b *Backend) sftpRead(ctx context.Context, machine, path string) ([]byte, error) {
	cli, cleanup, err := b.sftpClient(ctx, machine)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	f, err := cli.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", path, err)
	}
	defer f.Close()
	return io.ReadAll(f)
}

func (b *Backend) sftpWrite(ctx context.Context, machine, path string, data []byte) error {
	cli, cleanup, err := b.sftpClient(ctx, machine)
	if err != nil {
		return err
	}
	defer cleanup()
	dir := filepath.Dir(path)
	if st, err := cli.Stat(dir); err != nil || !st.IsDir() {
		if err := cli.MkdirAll(dir); err != nil {
			return fmt.Errorf("mkdir %q: %w", dir, err)
		}
	}
	tmp := path + ".tmp-write"
	f, err := cli.Create(tmp)
	if err != nil {
		return fmt.Errorf("stage %q: %w", tmp, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("write %q: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		return err
	}
	// atomic within the same directory (temp + rename)
	if err := cli.PosixRename(tmp, path); err != nil {
		return fmt.Errorf("rename %q: %w", path, err)
	}
	return nil
}

