package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Sriram-Nambiar/Phosphor/internal/config"
	"github.com/Sriram-Nambiar/Phosphor/internal/security"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

var (
	configCmd = &cobra.Command{
		Use:   "config",
		Short: "Inspect, validate, and troubleshoot Phosphor gateway configurations",
		Run: func(cmd *cobra.Command, args []string) {
			_ = cmd.Help()
		},
	}

	configCheckCmd = &cobra.Command{
		Use:     "check [config-file]",
		Aliases: []string{"validate", "test"},
		Short:   "Validate syntax, provider endpoints, routing policies, and security settings",
		Args:    cobra.MaximumNArgs(1),
		RunE:    runConfigCheck,
	}

	configViewCmd = &cobra.Command{
		Use:   "view [config-file]",
		Short: "Display resolved configuration schema with redacted credentials",
		Args:  cobra.MaximumNArgs(1),
		RunE:  runConfigView,
	}
)

func init() {
	configCmd.AddCommand(configCheckCmd)
	configCmd.AddCommand(configViewCmd)
	rootCmd.AddCommand(configCmd)
}

func resolveTargetConfigFile(args []string) string {
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		return strings.TrimSpace(args[0])
	}
	return GetConfigFile()
}

func runConfigCheck(cmd *cobra.Command, args []string) error {
	filePath := resolveTargetConfigFile(args)

	displayPath := filePath
	if displayPath == "" {
		displayPath = "default search locations (./config.yaml, ./config/config.yaml, ~/.phosphor/config.yaml)"
	} else if _, err := os.Stat(filePath); err != nil {
		renderConfigCheckFailure(filePath, err)
		return fmt.Errorf("config file not found: %w", err)
	}

	cfg, err := config.LoadConfig(filePath)
	if err != nil {
		renderConfigCheckFailure(displayPath, err)
		return fmt.Errorf("configuration validation failed: %w", err)
	}

	renderConfigCheckSuccess(displayPath, cfg)
	return nil
}

func runConfigView(cmd *cobra.Command, args []string) error {
	filePath := resolveTargetConfigFile(args)
	if filePath != "" {
		if _, err := os.Stat(filePath); err != nil {
			return fmt.Errorf("config file not found: %w", err)
		}
	}
	cfg, err := config.LoadConfig(filePath)
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Make a shallow copy of config with redacted keys for safe terminal printing
	safeCfg := *cfg
	safeCfg.Providers = make([]config.ProviderConfig, len(cfg.Providers))
	for i, p := range cfg.Providers {
		pCopy := p
		if pCopy.APIKey != "" {
			pCopy.APIKey = security.RedactAPIKey(pCopy.APIKey)
		}
		safeCfg.Providers[i] = pCopy
	}

	if safeCfg.Auth.Keys != nil {
		safeKeys := make([]config.APIKeyConfig, len(cfg.Auth.Keys))
		for i, k := range cfg.Auth.Keys {
			kCopy := k
			if kCopy.Key != "" {
				kCopy.Key = security.RedactAPIKey(kCopy.Key)
			}
			safeKeys[i] = kCopy
		}
		safeCfg.Auth.Keys = safeKeys
	}

	data, err := json.MarshalIndent(safeCfg, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to format configuration: %w", err)
	}

	fmt.Println(string(data))
	return nil
}

func renderConfigCheckSuccess(path string, cfg *config.Config) {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FAFAFA")).Background(lipgloss.Color("#059669")).Padding(0, 2)
	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#9CA3AF")).Width(22)
	valStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#10B981")).Bold(true)
	infoStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#E5E7EB"))

	absPath, _ := filepath.Abs(path)

	fmt.Println()
	fmt.Println(titleStyle.Render("CONFIGURATION VALID"))
	fmt.Printf("%s %s\n", labelStyle.Render("Source File:"), infoStyle.Render(absPath))
	fmt.Printf("%s %s\n", labelStyle.Render("Gateway Address:"), valStyle.Render(fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)))
	fmt.Printf("%s %s\n", labelStyle.Render("Database Path:"), infoStyle.Render(cfg.Database.Path))

	enabledProviders := 0
	for _, p := range cfg.Providers {
		if p.Enabled {
			enabledProviders++
		}
	}
	fmt.Printf("%s %d configured (%d enabled, %d disabled)\n",
		labelStyle.Render("Upstream Providers:"), len(cfg.Providers), enabledProviders, len(cfg.Providers)-enabledProviders)

	fmt.Printf("%s %d rules configured\n", labelStyle.Render("Model Routing Rules:"), len(cfg.Models))

	secStatus := "disabled"
	if cfg.Security.EnablePromptGuard {
		secStatus = fmt.Sprintf("enabled (threshold %.2f)", cfg.Security.BlockThreshold)
	}
	fmt.Printf("%s %s (allowed IPs: %d, blocked IPs: %d)\n",
		labelStyle.Render("Security Guard:"), secStatus, len(cfg.Security.AllowedIPs), len(cfg.Security.BlockedIPs))

	cacheStatus := "disabled"
	if cfg.Cache.Enabled {
		cacheStatus = fmt.Sprintf("enabled (capacity %d, ttl %v)", cfg.Cache.Capacity, cfg.Cache.TTL)
	}
	fmt.Printf("%s %s\n", labelStyle.Render("Prompt Cache:"), cacheStatus)

	authStatus := "disabled"
	if cfg.Auth.Enabled {
		authStatus = fmt.Sprintf("enabled (%d client keys)", len(cfg.Auth.Keys))
	}
	fmt.Printf("%s %s\n", labelStyle.Render("Client Authentication:"), authStatus)
	fmt.Println()
}

func renderConfigCheckFailure(path string, err error) {
	errTitle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FAFAFA")).Background(lipgloss.Color("#DC2626")).Padding(0, 2)
	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#9CA3AF")).Width(16)

	fmt.Println()
	fmt.Println(errTitle.Render("CONFIGURATION INVALID"))
	fmt.Printf("%s %s\n", labelStyle.Render("Source:"), path)
	fmt.Printf("%s %v\n", labelStyle.Render("Diagnosis:"), err)
	fmt.Println()
}
