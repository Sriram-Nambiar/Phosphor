package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Sriram-Nambiar/Phosphor/internal/budget"
	"github.com/Sriram-Nambiar/Phosphor/internal/config"
	"github.com/Sriram-Nambiar/Phosphor/internal/db"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

var (
	budgetsURL    string
	budgetsDBPath string
	budgetsAPIKey string

	budgetsCmd = &cobra.Command{
		Use:   "budgets",
		Short: "Inspect client spend limits, usage, and budget statuses",
		RunE:  runBudgets,
	}
)

func init() {
	budgetsCmd.Flags().StringVar(&budgetsURL, "url", "", "Phosphor gateway URL (default http://127.0.0.1:<port>)")
	budgetsCmd.Flags().StringVar(&budgetsDBPath, "db", "", "Path to SQLite database file")
	budgetsCmd.Flags().StringVar(&budgetsAPIKey, "key", "", "Admin API key for authentication")

	rootCmd.AddCommand(budgetsCmd)
}

type clientBudgetStatus struct {
	ClientName     string  `json:"client_name"`
	MaxSpend       float64 `json:"max_spend"`
	SoftLimit      float64 `json:"soft_limit"`
	CurrentSpend   float64 `json:"current_spend"`
	ResetPeriod    string  `json:"reset_period"`
	WindowStart    string  `json:"window_start,omitempty"`
	RemainingSpend float64 `json:"remaining_spend"`
	SoftReached    bool    `json:"soft_reached"`
	HardExceeded   bool    `json:"hard_exceeded"`
}

type adminBudgetsResponse struct {
	Object string               `json:"object"`
	Data   []clientBudgetStatus `json:"data"`
}

func runBudgets(cmd *cobra.Command, args []string) error {
	baseURL := budgetsURL
	if baseURL == "" {
		cfg, err := config.LoadConfig(GetConfigFile())
		if err == nil && cfg != nil && cfg.Server.Port > 0 {
			host := cfg.Server.Host
			if host == "" || host == "0.0.0.0" {
				host = "127.0.0.1"
			}
			baseURL = fmt.Sprintf("http://%s:%d", host, cfg.Server.Port)
		} else {
			baseURL = "http://127.0.0.1:8080"
		}
	}
	baseURL = strings.TrimRight(baseURL, "/")
	endpoint := baseURL + "/v1/admin/budgets"

	client := &http.Client{Timeout: 3 * time.Second}
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	if budgetsAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+budgetsAPIKey)
	}

	resp, err := client.Do(req)
	if err == nil && resp.StatusCode == http.StatusOK {
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		var budgetsResp adminBudgetsResponse
		if err := json.Unmarshal(body, &budgetsResp); err == nil {
			renderBudgets(budgetsResp.Data, baseURL)
			return nil
		}
	}

	// Fallback to offline direct calculation from config and DB
	return runBudgetsOffline()
}

func runBudgetsOffline() error {
	cfg, err := config.LoadConfig(GetConfigFile())
	if err != nil {
		return fmt.Errorf("gateway unreachable and failed to load config: %w", err)
	}

	dbPath := cfg.Database.Path
	if budgetsDBPath != "" {
		dbPath = budgetsDBPath
	}

	database, err := db.New(dbPath)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer database.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var statuses []clientBudgetStatus
	for _, k := range cfg.Auth.Keys {
		if k.Budget == nil || k.Budget.MaxSpend <= 0 {
			continue
		}

		windowStart := budget.WindowStart(k.Budget.ResetPeriod, time.Now().UTC())
		spend, err := database.GetClientSpend(ctx, k.Name, windowStart)
		if err != nil {
			spend = 0
		}

		eval := budget.Evaluate(k.Budget, spend)
		rem := k.Budget.MaxSpend - spend
		if rem < 0 {
			rem = 0
		}

		windowStr := ""
		if !windowStart.IsZero() {
			windowStr = windowStart.Format(time.RFC3339)
		}

		statuses = append(statuses, clientBudgetStatus{
			ClientName:     k.Name,
			MaxSpend:       k.Budget.MaxSpend,
			SoftLimit:      k.Budget.SoftLimit,
			CurrentSpend:   spend,
			ResetPeriod:    k.Budget.ResetPeriod,
			WindowStart:    windowStr,
			RemainingSpend: rem,
			SoftReached:    eval.SoftReached,
			HardExceeded:   !eval.Allowed,
		})
	}

	renderBudgets(statuses, "Local Configuration & Database")
	return nil
}

func renderBudgets(budgets []clientBudgetStatus, source string) {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FAFAFA")).Background(lipgloss.Color("#7D56F4")).Padding(0, 2)
	fmt.Println(titleStyle.Render("CLIENT BUDGETS & SPEND TRACKING"))
	fmt.Printf("Source: %s\n\n", source)

	if len(budgets) == 0 {
		fmt.Println("  No active client budget limits configured.")
		return
	}

	clientCol := lipgloss.NewStyle().Bold(true).Width(18)
	spendCol := lipgloss.NewStyle().Width(14)
	maxCol := lipgloss.NewStyle().Width(14)
	remCol := lipgloss.NewStyle().Width(14)
	periodCol := lipgloss.NewStyle().Width(12)
	statusCol := lipgloss.NewStyle().Width(16)

	header := fmt.Sprintf("  %s %s %s %s %s %s",
		clientCol.Render("CLIENT"),
		spendCol.Render("CURRENT SPEND"),
		maxCol.Render("MAX BUDGET"),
		remCol.Render("REMAINING"),
		periodCol.Render("PERIOD"),
		statusCol.Render("STATUS"),
	)
	fmt.Println(lipgloss.NewStyle().Foreground(lipgloss.Color("#888888")).Underline(true).Render(header))

	for _, b := range budgets {
		statusText := "OK"
		statusStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#00FFA3"))

		if b.HardExceeded {
			statusText = "EXCEEDED (BLOCKED)"
			statusStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FF4444"))
		} else if b.SoftReached {
			statusText = "SOFT CAP REACHED"
			statusStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FFAA00"))
		}

		periodStr := b.ResetPeriod
		if periodStr == "" {
			periodStr = "total"
		}

		row := fmt.Sprintf("  %s %s %s %s %s %s",
			clientCol.Render(b.ClientName),
			spendCol.Render(fmt.Sprintf("$%.4f", b.CurrentSpend)),
			maxCol.Render(fmt.Sprintf("$%.2f", b.MaxSpend)),
			remCol.Render(fmt.Sprintf("$%.4f", b.RemainingSpend)),
			periodCol.Render(periodStr),
			statusStyle.Render(statusCol.Render(statusText)),
		)
		fmt.Println(row)
	}
	fmt.Println()
}
