package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Sriram-Nambiar/Phosphor/internal/config"
	"github.com/Sriram-Nambiar/Phosphor/internal/db"
	"github.com/Sriram-Nambiar/Phosphor/internal/proxy"
	"github.com/Sriram-Nambiar/Phosphor/internal/router"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

var (
	startHost string
	startPort int
	startDB   string

	startCmd = &cobra.Command{
		Use:   "start",
		Short: "Start the Phosphor LLM gateway reverse proxy server",
		RunE:  runStart,
	}
)

func init() {
	startCmd.Flags().StringVarP(&startHost, "host", "H", "", "Host to bind server (e.g. 127.0.0.1)")
	startCmd.Flags().IntVarP(&startPort, "port", "p", 0, "Port to listen on (e.g. 8080)")
	startCmd.Flags().StringVar(&startDB, "db", "", "Path to SQLite database file")
	rootCmd.AddCommand(startCmd)
}

func runStart(cmd *cobra.Command, args []string) error {
	cfg, err := config.LoadConfig(GetConfigFile())
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	if startHost != "" {
		cfg.Server.Host = startHost
	}
	if startPort > 0 {
		cfg.Server.Port = startPort
	}
	if startDB != "" {
		cfg.Database.Path = startDB
	}

	// Initialize database
	database, err := db.New(cfg.Database.Path)
	if err != nil {
		return fmt.Errorf("failed to initialize database (%s): %w", cfg.Database.Path, err)
	}
	defer database.Close()

	// Initialize router
	r, err := router.NewRouter(cfg, database)
	if err != nil {
		return fmt.Errorf("failed to initialize router: %w", err)
	}

	// Initialize proxy server
	srv := proxy.NewServer(cfg, r, database)

	// Display stylish startup banner
	renderStartupBanner(cfg)

	// Graceful shutdown channel
	stopChan := make(chan os.Signal, 1)
	signal.Notify(stopChan, os.Interrupt, syscall.SIGTERM)

	serverErrChan := make(chan error, 1)
	go func() {
		if err := srv.Start(); err != nil && err != http.ErrServerClosed {
			serverErrChan <- err
		}
	}()

	select {
	case err := <-serverErrChan:
		return fmt.Errorf("server error: %w", err)
	case sig := <-stopChan:
		log.Printf("\n[Phosphor] Received signal %v. Initiating graceful shutdown...\n", sig)
		timeout := cfg.Server.ShutdownTimeout
		if timeout <= 0 {
			timeout = 15 * time.Second
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			log.Printf("[Phosphor] Shutdown error: %v\n", err)
		}
		log.Println("[Phosphor] Server stopped gracefully.")
	}

	return nil
}

func renderStartupBanner(cfg *config.Config) {
	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#FAFAFA")).
		Background(lipgloss.Color("#7D56F4")).
		Padding(0, 1)

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#7D56F4")).
		Padding(1, 2)

	accentStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#04B575")).Bold(true)
	subStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#888888"))

	var provList []string
	for _, p := range cfg.Providers {
		if p.Enabled {
			provList = append(provList, p.Name)
		}
	}

	content := fmt.Sprintf("%s\n\n", titleStyle.Render("⚡ PHOSPHOR LLM GATEWAY"))
	content += fmt.Sprintf("• %s: %s\n", accentStyle.Render("Listening"), fmt.Sprintf("http://%s:%d", cfg.Server.Host, cfg.Server.Port))
	content += fmt.Sprintf("• %s: %s\n", accentStyle.Render("Database"), cfg.Database.Path)
	content += fmt.Sprintf("• %s: %s\n", accentStyle.Render("Strategy"), string(cfg.Routing.DefaultStrategy))
	content += fmt.Sprintf("• %s: %v\n\n", accentStyle.Render("Providers"), provList)
	content += fmt.Sprintf("%s\n", subStyle.Render("Endpoints:"))
	content += fmt.Sprintf("  POST /v1/chat/completions (JSON & Streaming SSE)\n")
	content += fmt.Sprintf("  GET  /v1/models\n")
	content += fmt.Sprintf("  GET  /health\n")

	fmt.Println(boxStyle.Render(content))
}
