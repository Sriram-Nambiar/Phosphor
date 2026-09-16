package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Sriram-Nambiar/Phosphor/internal/config"
	"github.com/Sriram-Nambiar/Phosphor/internal/db"
	"github.com/charmbracelet/lipgloss"
	"github.com/dustin/go-humanize"
	"github.com/spf13/cobra"
)

var (
	cacheURL    string
	cacheDBPath string
	cacheAPIKey string

	cacheCmd = &cobra.Command{
		Use:   "cache",
		Short: "Manage and inspect prompt response cache",
		Run: func(cmd *cobra.Command, args []string) {
			_ = cmd.Help()
		},
	}

	cacheStatsCmd = &cobra.Command{
		Use:   "stats",
		Short: "Display prompt cache hit/miss statistics and capacity",
		RunE:  runCacheStats,
	}

	cacheClearCmd = &cobra.Command{
		Use:   "clear",
		Short: "Purge all cached responses from memory and database",
		RunE:  runCacheClear,
	}
)

func init() {
	cacheCmd.PersistentFlags().StringVar(&cacheURL, "url", "", "Phosphor gateway URL (default http://127.0.0.1:<port>)")
	cacheCmd.PersistentFlags().StringVar(&cacheDBPath, "db", "", "Path to SQLite database file")
	cacheCmd.PersistentFlags().StringVar(&cacheAPIKey, "key", "", "Admin API key for authentication")

	cacheCmd.AddCommand(cacheStatsCmd)
	cacheCmd.AddCommand(cacheClearCmd)
	rootCmd.AddCommand(cacheCmd)
}

type cacheStatsResponse struct {
	Enabled    bool    `json:"enabled"`
	Capacity   int     `json:"capacity"`
	Size       int     `json:"size"`
	Hits       int64   `json:"hits"`
	Misses     int64   `json:"misses"`
	HitRatio   float64 `json:"hit_ratio"`
	TTLSeconds int     `json:"ttl_seconds"`
}

type cacheClearResponse struct {
	Status        string `json:"status"`
	ClearedL1     int    `json:"cleared_l1"`
	ClearedL2Rows int64  `json:"cleared_l2_rows"`
}

func resolveGatewayURL() string {
	if cacheURL != "" {
		return strings.TrimRight(cacheURL, "/")
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

func runCacheStats(cmd *cobra.Command, args []string) error {
	baseURL := resolveGatewayURL()
	endpoint := baseURL + "/v1/admin/cache/stats"

	client := &http.Client{Timeout: 3 * time.Second}
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	if cacheAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cacheAPIKey)
	}

	resp, err := client.Do(req)
	if err == nil && resp.StatusCode == http.StatusOK {
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		var stats cacheStatsResponse
		if err := json.Unmarshal(body, &stats); err == nil {
			renderCacheStats(stats, baseURL)
			return nil
		}
	}

	// Fallback to direct DB query if gateway is offline
	return runCacheStatsOffline()
}

func runCacheStatsOffline() error {
	cfg, err := config.LoadConfig(GetConfigFile())
	if err != nil {
		return fmt.Errorf("gateway unreachable and failed to load config: %w", err)
	}

	dbPath := cfg.Database.Path
	if cacheDBPath != "" {
		dbPath = cacheDBPath
	}

	database, err := db.New(dbPath)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer database.Close()

	_, _ = database.PruneExpiredCache(context.Background())

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FAFAFA")).Background(lipgloss.Color("#7D56F4")).Padding(0, 2)
	fmt.Println(titleStyle.Render("PHOSPHOR CACHE - OFFLINE INSPECTION"))
	fmt.Printf("Database: %s\n", dbPath)

	size, _ := database.FileSize()
	fmt.Printf("Database File Size: %s\n", humanize.Bytes(uint64(size)))
	return nil
}

func renderCacheStats(stats cacheStatsResponse, sourceURL string) {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FAFAFA")).Background(lipgloss.Color("#7D56F4")).Padding(0, 2)
	labelStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#888888")).Width(22)
	valStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#00FFA3"))

	fmt.Println(titleStyle.Render("PHOSPHOR RESPONSE CACHE"))
	fmt.Printf("Gateway: %s\n\n", sourceURL)

	statusStr := "Enabled"
	if !stats.Enabled {
		statusStr = "Disabled"
	}
	fmt.Printf("  %s %s\n", labelStyle.Render("Status:"), valStyle.Render(statusStr))
	fmt.Printf("  %s %d items\n", labelStyle.Render("L1 Capacity:"), stats.Capacity)
	fmt.Printf("  %s %d items\n", labelStyle.Render("Current L1 Size:"), stats.Size)
	fmt.Printf("  %s %s\n", labelStyle.Render("Cache Hits:"), humanize.Comma(stats.Hits))
	fmt.Printf("  %s %s\n", labelStyle.Render("Cache Misses:"), humanize.Comma(stats.Misses))
	fmt.Printf("  %s %.1f%%\n", labelStyle.Render("Hit Ratio:"), stats.HitRatio*100.0)
	fmt.Printf("  %s %d seconds\n", labelStyle.Render("Configured TTL:"), stats.TTLSeconds)
	fmt.Println()
}

func runCacheClear(cmd *cobra.Command, args []string) error {
	baseURL := resolveGatewayURL()
	endpoint := baseURL + "/v1/admin/cache/clear"

	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest(http.MethodPost, endpoint, nil)
	if err != nil {
		return err
	}
	if cacheAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cacheAPIKey)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to contact gateway at %s: %w", baseURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("gateway returned error status %d: %s", resp.StatusCode, string(body))
	}

	var clearResp cacheClearResponse
	if err := json.NewDecoder(resp.Body).Decode(&clearResp); err != nil {
		return fmt.Errorf("failed to decode response: %w", err)
	}

	successStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#00FFA3"))
	fmt.Printf("%s Successfully purged cache:\n", successStyle.Render("✓"))
	fmt.Printf("  - Cleared %d items from L1 in-memory cache\n", clearResp.ClearedL1)
	fmt.Printf("  - Cleared %d rows from L2 SQLite database cache\n", clearResp.ClearedL2Rows)
	return nil
}
