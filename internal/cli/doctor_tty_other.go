//go:build !linux

package cli

import "os"

// isTerminal answers false off Linux: --fix then asks for --yes instead of
// prompting, which is the safe direction for a command that changes a host.
func isTerminal(*os.File) bool { return false }
