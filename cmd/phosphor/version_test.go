package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersionCommand(t *testing.T) {
	buf := new(bytes.Buffer)
	versionCmd.SetOut(buf)

	// Run version command
	versionCmd.Run(versionCmd, []string{})

	// Check that rootCmd has "version" command
	cmd, _, err := rootCmd.Find([]string{"version"})
	if err != nil || cmd == nil {
		t.Fatalf("failed to find version subcommand on rootCmd: %v", err)
	}
	if cmd.Name() != "version" {
		t.Errorf("expected command name 'version', got %s", cmd.Name())
	}
	if !strings.Contains(cmd.Short, "version") {
		t.Errorf("expected Short description to mention version, got %s", cmd.Short)
	}
}
