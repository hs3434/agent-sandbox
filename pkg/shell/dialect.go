// Package shell holds the shell-facing primitives the tool face drives:
// command dialects (POSIX vs PowerShell), ripgrep-backed search, and
// persistent sessions with marker framing.
//
// The designs here are ported from the Python predecessor (sandbox-mcp):
// search runs ripgrep ON THE TARGET (not a host-side walk), and a session
// frames command completion with a unique marker instead of a PTY prompt.
package shell

import (
	"fmt"
	"strings"
)

// Dialect is the OS-specific shell surface. Two implementations: POSIX
// (linux) and PowerShell (windows).
type Dialect interface {
	// Name identifies the dialect ("posix" / "powershell").
	Name() string
	// ShellArgs are the argv that starts a persistent session shell.
	ShellArgs() []string
	// FrameCommand wraps a command so its completion (and exit code) is
	// announced by a unique marker on stdout.
	FrameCommand(cmd, marker string) string
	// SearchFilesCommand lists files matching a glob (rg --files).
	SearchFilesCommand(pattern, path string, limit int) string
	// SearchContentCommand greps content (rg, with optional glob/context/mode).
	SearchContentCommand(pattern, path, fileGlob string, limit int, outputMode string, context int) string
	// SearchFilesCommandFallback / SearchContentCommandFallback are used when
	// ripgrep is absent on the target (stock images ship grep, not rg).
	SearchFilesCommandFallback(pattern, path string, limit int) string
	SearchContentCommandFallback(pattern, path, fileGlob string, limit int, outputMode string, context int) string
	// MissingToolHint tells the agent how to install ripgrep on this OS.
	MissingToolHint() string
}

// PosixDialect is bash/sh on linux (and macOS).
type PosixDialect struct{}

func (PosixDialect) Name() string { return "posix" }

func (PosixDialect) ShellArgs() []string { return []string{"sh"} }

// FrameCommand prints the marker and the exit status after the command:
//
//	<cmd>; printf '__SB_END_<marker>__:%s\n' "$?"
//
// The drain loop watches for the marker; the number after ':' is the exit code.
func (PosixDialect) FrameCommand(cmd, marker string) string {
	return fmt.Sprintf("%s; printf '__SB_END_%s__:%%s\\n' \"$?\"", cmd, marker)
}

func (PosixDialect) SearchFilesCommand(pattern, path string, limit int) string {
	glob := pattern
	if !strings.Contains(glob, "/") && !strings.HasPrefix(glob, "*") {
		glob = "*" + glob
	}
	return fmt.Sprintf("rg --files --sortr=modified -g %s %s 2>&1 | head -n %d",
		shellQuote(glob), shellQuote(path), limit)
}

func (PosixDialect) SearchContentCommand(pattern, path, fileGlob string, limit int, outputMode string, context int) string {
	var b strings.Builder
	b.WriteString("rg --line-number --no-heading --with-filename")
	if context > 0 {
		fmt.Fprintf(&b, " -C %d", context)
	}
	if fileGlob != "" {
		fmt.Fprintf(&b, " --glob %s", shellQuote(fileGlob))
	}
	switch outputMode {
	case "files_only":
		b.WriteString(" -l")
	case "count":
		b.WriteString(" -c")
	}
	fmt.Fprintf(&b, " %s %s 2>&1 | head -n %d", shellQuote(pattern), shellQuote(path), limit)
	return b.String()
}

func (PosixDialect) SearchFilesCommandFallback(pattern, path string, limit int) string {
	glob := pattern
	if !strings.Contains(glob, "/") && !strings.HasPrefix(glob, "*") {
		glob = "*" + glob
	}
	return fmt.Sprintf("find %s -type f -name %s 2>&1 | head -n %d",
		shellQuote(path), shellQuote(glob), limit)
}

func (PosixDialect) SearchContentCommandFallback(pattern, path, fileGlob string, limit int, outputMode string, context int) string {
	var b strings.Builder
	switch outputMode {
	case "files_only":
		b.WriteString("grep -rl")
	case "count":
		b.WriteString("grep -rc")
	default:
		b.WriteString("grep -rn")
	}
	if fileGlob != "" {
		fmt.Fprintf(&b, " --include=%s", shellQuote(fileGlob))
	}
	if context > 0 {
		fmt.Fprintf(&b, " -C %d", context)
	}
	fmt.Fprintf(&b, " -E %s %s 2>&1 | head -n %d", shellQuote(pattern), shellQuote(path), limit)
	return b.String()
}

func (PosixDialect) MissingToolHint() string {
	return "Install ripgrep on the target machine, e.g. one of:\n" +
		"  Linux:   apt install ripgrep / dnf install ripgrep / apk add ripgrep\n" +
		"  macOS:   brew install ripgrep\n" +
		"Then retry the file_search call."
}

// PowerShellDialect is windows (PowerShell in stdin-script mode — no PTY,
// AD-028).
type PowerShellDialect struct{}

func (PowerShellDialect) Name() string { return "powershell" }

func (PowerShellDialect) ShellArgs() []string {
	return []string{"powershell", "-NoProfile", "-NonInteractive", "-Command", "-"}
}

// FrameCommand announces completion with the marker and $LASTEXITCODE.
func (PowerShellDialect) FrameCommand(cmd, marker string) string {
	return fmt.Sprintf("%s; echo __SB_END_%s__:$LASTEXITCODE", cmd, marker)
}

func (PowerShellDialect) SearchFilesCommand(pattern, path string, limit int) string {
	glob := pattern
	if !strings.Contains(glob, "/") && !strings.HasPrefix(glob, "*") {
		glob = "*" + glob
	}
	return fmt.Sprintf("rg --files --sortr=modified -g %s %s 2>&1 | Select-Object -First %d",
		psQuote(glob), psQuote(path), limit)
}

func (PowerShellDialect) SearchContentCommand(pattern, path, fileGlob string, limit int, outputMode string, context int) string {
	var b strings.Builder
	b.WriteString("rg --line-number --no-heading --with-filename")
	if context > 0 {
		fmt.Fprintf(&b, " -C %d", context)
	}
	if fileGlob != "" {
		fmt.Fprintf(&b, " --glob %s", psQuote(fileGlob))
	}
	switch outputMode {
	case "files_only":
		b.WriteString(" -l")
	case "count":
		b.WriteString(" -c")
	}
	fmt.Fprintf(&b, " %s %s 2>&1 | Select-Object -First %d", psQuote(pattern), psQuote(path), limit)
	return b.String()
}

func (PowerShellDialect) SearchFilesCommandFallback(pattern, path string, limit int) string {
	glob := pattern
	if !strings.Contains(glob, "/") && !strings.HasPrefix(glob, "*") {
		glob = "*" + glob
	}
	return fmt.Sprintf("Get-ChildItem -Path %s -Recurse -File -Filter %s | Select-Object -First %d -ExpandProperty FullName",
		psQuote(path), psQuote(glob), limit)
}

func (PowerShellDialect) SearchContentCommandFallback(pattern, path, fileGlob string, limit int, outputMode string, context int) string {
	var b strings.Builder
	b.WriteString("Select-String -Path ")
	if fileGlob != "" {
		fmt.Fprintf(&b, "%s ", psQuote(path+"\\"+fileGlob))
	} else {
		fmt.Fprintf(&b, "%s ", psQuote(path))
	}
	fmt.Fprintf(&b, "-Pattern %s", psQuote(pattern))
	switch outputMode {
	case "files_only":
		b.WriteString(" | Select-Object -ExpandProperty Path -Unique")
	case "count":
		b.WriteString(" | Group-Object Path | Select-Object Name,Count")
	}
	fmt.Fprintf(&b, " | Select-Object -First %d", limit)
	return b.String()
}

func (PowerShellDialect) MissingToolHint() string {
	return "Install ripgrep on the target machine, e.g. one of:\n" +
		"  Windows:   winget install BurntSushi.ripgrep  " +
		"(or: choco install ripgrep, scoop install ripgrep)\n" +
		"Then retry the file_search call."
}

// DialectFor picks the dialect from an OS type ("linux"/"windows").
func DialectFor(osType string) Dialect {
	if strings.EqualFold(osType, "windows") {
		return PowerShellDialect{}
	}
	return PosixDialect{}
}

// Quote is the exported quoting contract (D-30): any command we PRINT in
// advice/install instructions must survive a human paste. POSIX dialect.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// shellQuote single-quotes for POSIX shells.
func shellQuote(s string) string {
	return Quote(s)
}

// psQuote single-quotes for PowerShell (doubles internal quotes).
func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
