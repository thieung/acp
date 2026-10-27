package core

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
)

// BinaryMarker is a string every acp binary carries, so install.sh and the hook check can tell
// acp apart from another program without running it.
const BinaryMarker = "ACP-BINARY-v1"

// The session-start hook command, in the two forms acp ever wrote:
//
//	'<path>/acp' link --hook            the Go binary (quoted only when the path needs it)
//	python3 <root>/scripts/acp link --hook   the retired Python script
//
// Recognition is by the whole command, and a binary not called acp must carry BinaryMarker (ACP_BIN
// may name it anything), so a user's own hook such as `my-acp link --hook` is never taken for acp's.
var (
	hookCommand = regexp.MustCompile(`^(?:'([^']+)'|([^\s'"]+)) link --hook$`)
	legacyHook  = regexp.MustCompile(`^python3 (?:'[^']+'|[^\s'"]+)/scripts/acp link --hook$`)
)

// HookBinary returns the binary path of an acp hook command, or "" when the command is not one.
func HookBinary(command string) string {
	m := hookCommand.FindStringSubmatch(command)
	if m == nil {
		return ""
	}
	path := m[1] + m[2]
	if filepath.Base(path) == "acp" {
		return path
	}
	if data, err := os.ReadFile(path); err == nil && bytes.Contains(data, []byte(BinaryMarker)) {
		return path
	}
	return ""
}

// IsLegacyHook reports whether a command is the Python acp's session-start hook.
func IsLegacyHook(command string) bool { return legacyHook.MatchString(command) }

// IsAcpHook reports whether a command is either form of acp's hook.
func IsAcpHook(command string) bool { return HookBinary(command) != "" || IsLegacyHook(command) }
