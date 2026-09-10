package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/Sriram-Nambiar/Phosphor/internal/auth"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

var hashKeyCmd = &cobra.Command{
	Use:   "hash-key [key]",
	Short: "Compute SHA-256 hash of an API key or generate a secure random key with hash",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runHashKey,
}

func init() {
	rootCmd.AddCommand(hashKeyCmd)
}

func runHashKey(cmd *cobra.Command, args []string) error {
	var rawKey string
	if len(args) == 1 {
		rawKey = args[0]
	} else {
		// Generate random 24-byte key
		b := make([]byte, 24)
		if _, err := rand.Read(b); err != nil {
			return fmt.Errorf("failed to generate random key: %w", err)
		}
		rawKey = "ph_" + hex.EncodeToString(b)
	}

	hash := auth.HashKey(rawKey)

	labelStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7D56F4"))
	valueStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#04B575")).Bold(true)
	commentStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#888888"))

	fmt.Println(labelStyle.Render("Phosphor API Key Generator"))
	fmt.Println()
	if len(args) == 0 {
		fmt.Printf("Generated Key: %s\n", valueStyle.Render(rawKey))
		fmt.Println("Save this key now! It cannot be recovered from the hash.")
		fmt.Println()
	}
	fmt.Printf("SHA-256 Hash: %s\n", valueStyle.Render(hash))
	fmt.Println()
	fmt.Println(commentStyle.Render("Example YAML configuration snippet:"))
	fmt.Println(commentStyle.Render("auth:"))
	fmt.Println(commentStyle.Render("  enabled: true"))
	fmt.Println(commentStyle.Render("  keys:"))
	fmt.Printf("    - name: %q\n", "client-name")
	fmt.Printf("      key_hash: %q\n", hash)

	return nil
}
