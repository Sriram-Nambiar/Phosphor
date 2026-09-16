package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Sriram-Nambiar/Phosphor/internal/config"
	"github.com/Sriram-Nambiar/Phosphor/internal/db"
	"github.com/charmbracelet/lipgloss"
	"github.com/dustin/go-humanize"
	"github.com/spf13/cobra"
)

var (
	dbURL      string
	dbPathFlag string
	dbKeyFlag  string
	dbDestFlag string

	dbCmd = &cobra.Command{
		Use:   "db",
		Short: "Perform database maintenance, optimization, and backups",
		Run: func(cmd *cobra.Command, args []string) {
			_ = cmd.Help()
		},
	}

	dbBackupCmd = &cobra.Command{
		Use:   "backup [destination]",
		Short: "Create an atomic, consistent backup copy of the database",
		Args:  cobra.MaximumNArgs(1),
		RunE:  runDBBackup,
	}

	dbVacuumCmd = &cobra.Command{
		Use:   "vacuum",
		Short: "Defragment SQLite database and reclaim freed disk space",
		RunE:  runDBVacuum,
	}
)

func init() {
	dbCmd.PersistentFlags().StringVar(&dbURL, "url", "", "Phosphor gateway URL (default http://127.0.0.1:<port>)")
	dbCmd.PersistentFlags().StringVar(&dbPathFlag, "db", "", "Path to SQLite database file")
	dbCmd.PersistentFlags().StringVar(&dbKeyFlag, "key", "", "Admin API key for authentication")

	dbBackupCmd.Flags().StringVarP(&dbDestFlag, "dest", "d", "", "Destination file path for backup snapshot")

	dbCmd.AddCommand(dbBackupCmd)
	dbCmd.AddCommand(dbVacuumCmd)
	rootCmd.AddCommand(dbCmd)
}

func resolveDBGatewayURL() string {
	if dbURL != "" {
		return strings.TrimRight(dbURL, "/")
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

func runDBBackup(cmd *cobra.Command, args []string) error {
	dest := dbDestFlag
	if len(args) > 0 && args[0] != "" {
		dest = args[0]
	}
	if dest == "" {
		dest = fmt.Sprintf("phosphor_backup_%s.db", time.Now().UTC().Format("20060102_150405"))
	}

	// 1. Try Online Gateway API if --db flag is not explicitly given
	if dbPathFlag == "" {
		baseURL := resolveDBGatewayURL()
		endpoint := baseURL + "/v1/admin/db/backup"

		reqBody, _ := json.Marshal(map[string]string{"destination": dest})
		client := &http.Client{Timeout: 15 * time.Second}
		req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(reqBody))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
			if dbKeyFlag != "" {
				req.Header.Set("Authorization", "Bearer "+dbKeyFlag)
			}
			resp, err := client.Do(req)
			if err == nil {
				defer resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					var result struct {
						Status      string `json:"status"`
						Destination string `json:"destination"`
						SizeBytes   int64  `json:"size_bytes"`
					}
					if err := json.NewDecoder(resp.Body).Decode(&result); err == nil {
						renderDBBackupSuccess(result.Destination, result.SizeBytes, "Gateway Online")
						return nil
					}
				}
			}
		}
	}

	// 2. Direct Offline Database Backup
	cfg, err := config.LoadConfig(GetConfigFile())
	dbPath := ""
	if err == nil && cfg != nil {
		dbPath = cfg.Database.Path
	}
	if dbPathFlag != "" {
		dbPath = dbPathFlag
	}
	if dbPath == "" {
		dbPath = "phosphor.db"
	}

	database, err := db.New(dbPath)
	if err != nil {
		return fmt.Errorf("failed to open database at %s: %w", dbPath, err)
	}
	defer database.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := database.Backup(ctx, dest); err != nil {
		return fmt.Errorf("backup failed: %w", err)
	}

	var size int64
	if fi, err := os.Stat(dest); err == nil {
		size = fi.Size()
	}

	renderDBBackupSuccess(dest, size, "Direct SQLite")
	return nil
}

func runDBVacuum(cmd *cobra.Command, args []string) error {
	if dbPathFlag == "" {
		baseURL := resolveDBGatewayURL()
		endpoint := baseURL + "/v1/admin/db/vacuum"

		client := &http.Client{Timeout: 30 * time.Second}
		req, err := http.NewRequest(http.MethodPost, endpoint, nil)
		if err == nil {
			if dbKeyFlag != "" {
				req.Header.Set("Authorization", "Bearer "+dbKeyFlag)
			}
			resp, err := client.Do(req)
			if err == nil {
				defer resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					var result struct {
						Status    string `json:"status"`
						SizeBytes int64  `json:"size_bytes"`
					}
					if err := json.NewDecoder(resp.Body).Decode(&result); err == nil {
						renderDBVacuumSuccess(result.SizeBytes, "Gateway Online")
						return nil
					}
				}
			}
		}
	}

	cfg, err := config.LoadConfig(GetConfigFile())
	dbPath := ""
	if err == nil && cfg != nil {
		dbPath = cfg.Database.Path
	}
	if dbPathFlag != "" {
		dbPath = dbPathFlag
	}
	if dbPath == "" {
		dbPath = "phosphor.db"
	}

	database, err := db.New(dbPath)
	if err != nil {
		return fmt.Errorf("failed to open database at %s: %w", dbPath, err)
	}
	defer database.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := database.Vacuum(ctx); err != nil {
		return fmt.Errorf("vacuum failed: %w", err)
	}

	size, _ := database.FileSize()
	renderDBVacuumSuccess(size, "Direct SQLite")
	return nil
}

func renderDBBackupSuccess(dest string, size int64, mode string) {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FAFAFA")).Background(lipgloss.Color("#059669")).Padding(0, 2)
	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#9CA3AF")).Width(16)
	valStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#10B981")).Bold(true)

	fmt.Println()
	fmt.Println(titleStyle.Render("DATABASE BACKUP COMPLETED"))
	fmt.Printf("%s %s\n", labelStyle.Render("Destination:"), valStyle.Render(dest))
	fmt.Printf("%s %s\n", labelStyle.Render("Backup Size:"), humanize.Bytes(uint64(size)))
	fmt.Printf("%s %s\n", labelStyle.Render("Execution Mode:"), mode)
	fmt.Println()
}

func renderDBVacuumSuccess(size int64, mode string) {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FAFAFA")).Background(lipgloss.Color("#3B82F6")).Padding(0, 2)
	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#9CA3AF")).Width(16)

	fmt.Println()
	fmt.Println(titleStyle.Render("DATABASE VACUUM COMPLETED"))
	fmt.Printf("%s %s\n", labelStyle.Render("Current Size:"), humanize.Bytes(uint64(size)))
	fmt.Printf("%s %s\n", labelStyle.Render("Execution Mode:"), mode)
	fmt.Println()
}
