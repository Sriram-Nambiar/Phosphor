package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Sriram-Nambiar/Phosphor/internal/config"
	"github.com/Sriram-Nambiar/Phosphor/internal/db"
	"github.com/Sriram-Nambiar/Phosphor/internal/security"
	"github.com/charmbracelet/lipgloss"
	"github.com/dustin/go-humanize"
	"github.com/spf13/cobra"
)

var (
	logsDBPath        string
	logsLimit         int
	logsFailoversOnly bool
	logsClient        string
	logsModel         string
	logsProvider      string

	logsCmd = &cobra.Command{
		Use:   "logs",
		Short: "Inspect recent gateway requests and failover traces",
		RunE:  runLogs,
	}
)

func init() {
	logsCmd.Flags().StringVar(&logsDBPath, "db", "", "Path to SQLite database file")
	logsCmd.Flags().IntVarP(&logsLimit, "limit", "n", 20, "Number of records to display")
	logsCmd.Flags().BoolVarP(&logsFailoversOnly, "failovers", "f", false, "Display only failover traces")
	logsCmd.Flags().StringVar(&logsClient, "client", "", "Filter requests by client name")
	logsCmd.Flags().StringVar(&logsModel, "model", "", "Filter requests by model name")
	logsCmd.Flags().StringVar(&logsProvider, "provider", "", "Filter requests by provider name")
	rootCmd.AddCommand(logsCmd)
}

func runLogs(cmd *cobra.Command, args []string) error {
	cfg, err := config.LoadConfig(GetConfigFile())
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	dbPath := cfg.Database.Path
	if logsDBPath != "" {
		dbPath = logsDBPath
	}

	database, err := db.New(dbPath)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer database.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#FAFAFA")).
		Background(lipgloss.Color("#7D56F4")).
		Padding(0, 2)

	thStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FFFFFF")).Background(lipgloss.Color("#333333")).Padding(0, 1)
	successStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#04B575")).Bold(true)
	errorStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#FF5F87")).Bold(true)
	subStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#888888"))
	arrowStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#FFB86C")).Bold(true)

	fmt.Println()

	// Always show failover traces if requested or if there are recent ones
	failovers, err := database.GetRecentFailovers(ctx, logsLimit)
	if err == nil && len(failovers) > 0 {
		fmt.Println(headerStyle.Render("⚡ RECENT FAILOVER TRACES"))
		fmt.Printf("%-20s %-16s %-32s %-12s %s\n",
			thStyle.Render("TIMESTAMP"),
			thStyle.Render("REQUEST ID"),
			thStyle.Render("FAILOVER CASCADE"),
			thStyle.Render("LATENCY"),
			thStyle.Render("REASON"),
		)

		for _, f := range failovers {
			reqIDShort := f.RequestID
			if len(reqIDShort) > 14 {
				reqIDShort = reqIDShort[:14] + ".."
			}
			cascade := fmt.Sprintf("%s %s %s",
				errorStyle.Render(f.FromProvider),
				arrowStyle.Render("➔"),
				successStyle.Render(f.ToProvider),
			)
			fmt.Printf("%-20s %-16s %-42s %-12s %s\n",
				subStyle.Render(f.Timestamp.Format("15:04:05 02-Jan")),
				reqIDShort,
				cascade,
				fmt.Sprintf("%.1f ms", f.LatencyMs),
				errorStyle.Render(security.RedactText(f.Reason)),
			)
		}
		fmt.Println()
	}

	if logsFailoversOnly {
		return nil
	}

	// Requests query
	filter := db.RequestLogFilter{
		ClientName: logsClient,
		Model:      logsModel,
		Provider:   logsProvider,
		Limit:      logsLimit,
	}
	requests, err := database.QueryRequests(ctx, filter)
	if err != nil {
		return fmt.Errorf("failed to fetch requests: %w", err)
	}

	fmt.Println(headerStyle.Render("📋 RECENT GATEWAY REQUESTS"))
	var filterNotes []string
	if logsClient != "" {
		filterNotes = append(filterNotes, fmt.Sprintf("client=%s", logsClient))
	}
	if logsModel != "" {
		filterNotes = append(filterNotes, fmt.Sprintf("model=%s", logsModel))
	}
	if logsProvider != "" {
		filterNotes = append(filterNotes, fmt.Sprintf("provider=%s", logsProvider))
	}
	if len(filterNotes) > 0 {
		fmt.Printf("Filter applied: %s\n", strings.Join(filterNotes, ", "))
	}
	fmt.Println()

	if len(requests) == 0 {
		fmt.Println(subStyle.Render("No matching requests found in database."))
		fmt.Println()
		return nil
	}

	fmt.Printf("%-18s %-12s %-14s %-16s %-12s %-8s %-10s %-10s %-14s %s\n",
		thStyle.Render("TIME"),
		thStyle.Render("CLIENT"),
		thStyle.Render("MODEL REQ"),
		thStyle.Render("ROUTED TO"),
		thStyle.Render("PROVIDER"),
		thStyle.Render("STATUS"),
		thStyle.Render("TOKENS"),
		thStyle.Render("COST"),
		thStyle.Render("LAT / TTFT"),
		thStyle.Render("TYPE"),
	)

	for _, r := range requests {
		statusStr := successStyle.Render(fmt.Sprintf("%d", r.StatusCode))
		if r.StatusCode >= 400 {
			statusStr = errorStyle.Render(fmt.Sprintf("%d", r.StatusCode))
		}

		clientDisplay := r.ClientName
		if clientDisplay == "" {
			clientDisplay = "-"
		} else if len(clientDisplay) > 11 {
			clientDisplay = clientDisplay[:9] + ".."
		}

		typeStr := "[JSON]"
		if r.Stream {
			typeStr = "[SSE]"
		}

		latStr := fmt.Sprintf("%.0fms", r.LatencyMs)
		if r.TTFTMs > 0 {
			latStr = fmt.Sprintf("%.0f/%.0fms", r.LatencyMs, r.TTFTMs)
		}

		costStr := fmt.Sprintf("$%.5f", r.EstimatedCost)

		fmt.Printf("%-18s %-12s %-14s %-16s %-12s %-8s %-10s %-10s %-14s %s\n",
			subStyle.Render(r.CreatedAt.Format("15:04:05")),
			clientDisplay,
			r.ModelRequested,
			r.ModelRouted,
			r.Provider,
			statusStr,
			humanize.Comma(int64(r.TotalTokens)),
			costStr,
			latStr,
			typeStr,
		)
	}
	fmt.Println()

	return nil
}
