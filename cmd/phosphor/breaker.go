package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Sriram-Nambiar/Phosphor/internal/config"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

var (
	breakerURL     string
	breakerKeyFlag string

	breakerCmd = &cobra.Command{
		Use:   "breaker",
		Short: "Inspect and manage upstream provider circuit breakers",
		Run: func(cmd *cobra.Command, args []string) {
			_ = cmd.Help()
		},
	}

	breakerResetCmd = &cobra.Command{
		Use:   "reset [provider]",
		Short: "Reset tripped circuit breaker back to CLOSED (default: all)",
		Args:  cobra.MaximumNArgs(1),
		RunE:  runBreakerReset,
	}
)

func init() {
	breakerCmd.PersistentFlags().StringVar(&breakerURL, "url", "", "Phosphor gateway URL (default: from config or http://127.0.0.1:8080)")
	breakerCmd.PersistentFlags().StringVar(&breakerKeyFlag, "key", "", "Admin API key for authentication")

	breakerCmd.AddCommand(breakerResetCmd)
	rootCmd.AddCommand(breakerCmd)
}

func resolveBreakerGatewayURL() string {
	if breakerURL != "" {
		return strings.TrimRight(breakerURL, "/")
	}
	cfg, err := config.LoadConfig(GetConfigFile())
	if err == nil && cfg != nil && cfg.Server.Port > 0 {
		host := cfg.Server.Host
		if host == "" || host == "0.0.0.0" {
			host = "127.0.0.1"
		}
		return fmt.Sprintf("http://%s:%d", host, cfg.Server.Port)
	}
	return "http://127.0.0.1:8080"
}

func runBreakerReset(cmd *cobra.Command, args []string) error {
	provider := "all"
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		provider = strings.TrimSpace(args[0])
	}

	baseURL := resolveBreakerGatewayURL()
	endpoint := baseURL + "/v1/admin/circuit-breakers/reset"

	payload, err := json.Marshal(map[string]string{"provider": provider})
	if err != nil {
		return fmt.Errorf("failed to encode request: %w", err)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if breakerKeyFlag != "" {
		req.Header.Set("Authorization", "Bearer "+breakerKeyFlag)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("gateway connection failed: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("reset failed (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}

	var res struct {
		Status   string `json:"status"`
		Message  string `json:"message"`
		Provider string `json:"provider"`
	}
	_ = json.Unmarshal(bodyBytes, &res)

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("42"))
	fmt.Printf("%s Circuit breaker reset successfully for '%s'\n", titleStyle.Render("✔"), res.Provider)
	return nil
}
