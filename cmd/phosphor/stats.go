package main

import (
	"context"
	"fmt"
	"time"

	"github.com/Sriram-Nambiar/Phosphor/internal/config"
	"github.com/Sriram-Nambiar/Phosphor/internal/db"
	"github.com/charmbracelet/lipgloss"
	"github.com/dustin/go-humanize"
	"github.com/spf13/cobra"
)

var (
	statsDBPath string

	statsCmd = &cobra.Command{
		Use:   "stats",
		Short: "Display aggregate spend, token usage, and provider metrics",
		RunE:  runStats,
	}
)

func init() {
	statsCmd.Flags().StringVar(&statsDBPath, "db", "", "Path to SQLite database file")
	rootCmd.AddCommand(statsCmd)
}

func runStats(cmd *cobra.Command, args []string) error {
	cfg, err := config.LoadConfig(GetConfigFile())
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	dbPath := cfg.Database.Path
	if statsDBPath != "" {
		dbPath = statsDBPath
	}

	database, err := db.New(dbPath)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer database.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stats, err := database.GetAggregateStats(ctx)
	if err != nil {
		return fmt.Errorf("failed to get aggregate statistics: %w", err)
	}

	metrics, err := database.GetProviderMetrics(ctx)
	if err != nil {
		return fmt.Errorf("failed to get provider metrics: %w", err)
	}

	renderStatsDashboard(stats, metrics, dbPath)
	return nil
}

func renderStatsDashboard(stats *db.AggregateStats, metrics []db.ProviderMetric, dbPath string) {
	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#FAFAFA")).
		Background(lipgloss.Color("#7D56F4")).
		Padding(0, 2)

	cardStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#555555")).
		Padding(0, 1).
		Width(24)

	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#AAAAAA"))
	valueStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#04B575"))
	costStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#EE6FF8"))
	warnStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FF5F87"))

	fmt.Println()
	fmt.Println(headerStyle.Render("📊 PHOSPHOR TELEMETRY DASHBOARD"))
	fmt.Printf("Database: %s\n\n", dbPath)

	if stats.TotalRequests == 0 {
		emptyStyle := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#444444")).
			Padding(1, 2)
		fmt.Println(emptyStyle.Render("No requests logged yet.\nSend requests to http://127.0.0.1:8080/v1/chat/completions to collect telemetry."))
		fmt.Println()
		return
	}

	// 1. Metric Cards
	c1 := cardStyle.Render(fmt.Sprintf("%s\n%s", labelStyle.Render("Total Spend"), costStyle.Render(fmt.Sprintf("$%.6f", stats.TotalCost))))
	c2 := cardStyle.Render(fmt.Sprintf("%s\n%s", labelStyle.Render("Total Requests"), valueStyle.Render(humanize.Comma(int64(stats.TotalRequests)))))
	c3 := cardStyle.Render(fmt.Sprintf("%s\n%s", labelStyle.Render("Total Tokens"), valueStyle.Render(humanize.Comma(int64(stats.TotalTokens)))))

	c4 := cardStyle.Render(fmt.Sprintf("%s\n%s", labelStyle.Render("Total Failovers"), warnStyle.Render(fmt.Sprintf("%d", stats.TotalFailovers))))
	c5 := cardStyle.Render(fmt.Sprintf("%s\n%s", labelStyle.Render("Avg Latency"), valueStyle.Render(fmt.Sprintf("%.1f ms", stats.AvgLatencyMs))))
	c6 := cardStyle.Render(fmt.Sprintf("%s\n%s", labelStyle.Render("Avg TTFT"), valueStyle.Render(fmt.Sprintf("%.1f ms", stats.AvgTTFTMs))))

	row1 := lipgloss.JoinHorizontal(lipgloss.Top, c1, " ", c2, " ", c3)
	row2 := lipgloss.JoinHorizontal(lipgloss.Top, c4, " ", c5, " ", c6)

	fmt.Println(row1)
	fmt.Println(row2)
	fmt.Println()

	// 2. Provider Breakdown Table
	thStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FFFFFF")).Background(lipgloss.Color("#383838")).Padding(0, 1)
	tdStyle := lipgloss.NewStyle().Padding(0, 1)

	fmt.Println(lipgloss.NewStyle().Bold(true).Render("Provider Metrics & Health:"))

	headers := fmt.Sprintf("%-16s %-24s %-10s %-10s %-12s %-12s",
		thStyle.Render("PROVIDER"),
		thStyle.Render("MODEL"),
		thStyle.Render("REQUESTS"),
		thStyle.Render("FAILURES"),
		thStyle.Render("EMA TTFT"),
		thStyle.Render("EMA LATENCY"),
	)
	fmt.Println(headers)

	if len(metrics) == 0 {
		// Fall back to provider stats
		for p, pStat := range stats.ProviderStats {
			line := fmt.Sprintf("%-16s %-24s %-10s %-10s %-12s %-12s",
				tdStyle.Render(p),
				tdStyle.Render("*"),
				tdStyle.Render(fmt.Sprintf("%d", pStat.Requests)),
				tdStyle.Render(fmt.Sprintf("%d", pStat.Failures)),
				tdStyle.Render(fmt.Sprintf("%.1f ms", pStat.AvgTTFTMs)),
				tdStyle.Render(fmt.Sprintf("%.1f ms", pStat.AvgLatencyMs)),
			)
			fmt.Println(line)
		}
	} else {
		for _, m := range metrics {
			line := fmt.Sprintf("%-16s %-24s %-10s %-10s %-12s %-12s",
				tdStyle.Render(m.Provider),
				tdStyle.Render(m.Model),
				tdStyle.Render(fmt.Sprintf("%d", m.TotalRequests)),
				tdStyle.Render(fmt.Sprintf("%d", m.TotalFailures)),
				tdStyle.Render(fmt.Sprintf("%.1f ms", m.EMATTFTMs)),
				tdStyle.Render(fmt.Sprintf("%.1f ms", m.EMALatencyMs)),
			)
			fmt.Println(line)
		}
	}
	fmt.Println()
}
