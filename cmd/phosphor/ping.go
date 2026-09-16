package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Sriram-Nambiar/Phosphor/internal/config"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

var (
	pingURL      string
	pingCount    int
	pingInterval time.Duration
	pingTimeout  time.Duration
	pingJSON     bool

	pingCmd = &cobra.Command{
		Use:   "ping",
		Short: "Ping a running Phosphor gateway to verify liveness and measure latency",
		Long:  "Sends HTTP probes to a Phosphor gateway's health and readiness endpoints, reporting service state and RTT.",
		RunE:  runPing,
	}
)

func init() {
	pingCmd.Flags().StringVar(&pingURL, "url", "", "Phosphor gateway URL (default http://127.0.0.1:<port>)")
	pingCmd.Flags().IntVarP(&pingCount, "count", "c", 1, "Number of ping requests to send")
	pingCmd.Flags().DurationVarP(&pingInterval, "interval", "i", 1*time.Second, "Interval between pings")
	pingCmd.Flags().DurationVarP(&pingTimeout, "timeout", "t", 2*time.Second, "Timeout per ping request")
	pingCmd.Flags().BoolVar(&pingJSON, "json", false, "Output results as JSON")

	rootCmd.AddCommand(pingCmd)
}

type PingResult struct {
	Seq        int               `json:"seq"`
	URL        string            `json:"url"`
	StatusCode int               `json:"status_code"`
	Status     string            `json:"status"`
	LatencyMs  float64           `json:"latency_ms"`
	Database   string            `json:"database,omitempty"`
	Providers  map[string]string `json:"providers,omitempty"`
	Error      string            `json:"error,omitempty"`
}

type PingSummary struct {
	Target      string       `json:"target"`
	Transmitted int          `json:"transmitted"`
	Received    int          `json:"received"`
	LossPercent float64      `json:"loss_percent"`
	MinMs       float64      `json:"min_ms"`
	AvgMs       float64      `json:"avg_ms"`
	MaxMs       float64      `json:"max_ms"`
	Results     []PingResult `json:"results"`
}

func resolvePingBaseURL() string {
	baseURL := pingURL
	if baseURL != "" {
		return strings.TrimRight(baseURL, "/")
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

func runPing(cmd *cobra.Command, args []string) error {
	baseURL := resolvePingBaseURL()
	endpoint := baseURL + "/ready"

	if pingCount <= 0 {
		pingCount = 1
	}

	client := &http.Client{
		Timeout: pingTimeout,
	}

	var results []PingResult
	var latencies []float64

	if !pingJSON {
		titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FAFAFA")).Background(lipgloss.Color("#059669")).Padding(0, 2)
		fmt.Println(titleStyle.Render("PHOSPHOR GATEWAY PING"))
		fmt.Printf("Probing %s (%d request(s), timeout %v)\n\n", endpoint, pingCount, pingTimeout)
	}

	for seq := 1; seq <= pingCount; seq++ {
		result := doSinglePing(client, endpoint, seq)
		results = append(results, result)

		if result.StatusCode == http.StatusOK {
			latencies = append(latencies, result.LatencyMs)
		}

		if !pingJSON {
			printPingResultLine(result)
		}

		if seq < pingCount && pingInterval > 0 {
			time.Sleep(pingInterval)
		}
	}

	summary := computePingSummary(baseURL, results, latencies)

	if pingJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(summary)
	}

	printPingSummary(summary)

	if summary.Received == 0 {
		return fmt.Errorf("all ping probes to %s failed", baseURL)
	}
	return nil
}

func doSinglePing(client *http.Client, endpoint string, seq int) PingResult {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, endpoint, nil)
	if err != nil {
		return PingResult{
			Seq:   seq,
			URL:   endpoint,
			Error: err.Error(),
		}
	}

	start := time.Now()
	resp, err := client.Do(req)
	latency := float64(time.Since(start).Microseconds()) / 1000.0

	if err != nil {
		return PingResult{
			Seq:       seq,
			URL:       endpoint,
			LatencyMs: latency,
			Error:     err.Error(),
		}
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var bodyJSON struct {
		Status    string            `json:"status"`
		Database  string            `json:"database"`
		Providers map[string]string `json:"providers"`
	}
	_ = json.Unmarshal(body, &bodyJSON)

	status := bodyJSON.Status
	if status == "" {
		if resp.StatusCode == http.StatusOK {
			status = "ready"
		} else {
			status = resp.Status
		}
	}

	return PingResult{
		Seq:        seq,
		URL:        endpoint,
		StatusCode: resp.StatusCode,
		Status:     status,
		LatencyMs:  latency,
		Database:   bodyJSON.Database,
		Providers:  bodyJSON.Providers,
	}
}

func printPingResultLine(res PingResult) {
	if res.Error != "" {
		errStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444")).Bold(true)
		fmt.Printf("  seq=%d target=%s: %s (%.2fms)\n", res.Seq, res.URL, errStyle.Render("FAIL: "+res.Error), res.LatencyMs)
		return
	}

	var badge string
	switch res.Status {
	case "ready", "healthy":
		badge = lipgloss.NewStyle().Foreground(lipgloss.Color("#10B981")).Bold(true).Render(strings.ToUpper(res.Status))
	case "degraded":
		badge = lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B")).Bold(true).Render("DEGRADED")
	default:
		badge = lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444")).Bold(true).Render(strings.ToUpper(res.Status))
	}

	provSummary := ""
	if len(res.Providers) > 0 {
		provs := make([]string, 0, len(res.Providers))
		for p, st := range res.Providers {
			provs = append(provs, fmt.Sprintf("%s:%s", p, st))
		}
		provSummary = fmt.Sprintf(" [%s]", strings.Join(provs, ", "))
	}

	fmt.Printf("  seq=%d http=%d status=%s latency=%.2fms db=%s%s\n",
		res.Seq,
		res.StatusCode,
		badge,
		res.LatencyMs,
		res.Database,
		provSummary,
	)
}

func computePingSummary(target string, results []PingResult, latencies []float64) PingSummary {
	transmitted := len(results)
	received := len(latencies)

	loss := 0.0
	if transmitted > 0 {
		loss = float64(transmitted-received) / float64(transmitted) * 100.0
	}

	var minMs, maxMs, avgMs float64
	if received > 0 {
		minMs = latencies[0]
		maxMs = latencies[0]
		sum := 0.0
		for _, lat := range latencies {
			if lat < minMs {
				minMs = lat
			}
			if lat > maxMs {
				maxMs = lat
			}
			sum += lat
		}
		avgMs = sum / float64(received)
	}

	// Round values
	minMs = math.Round(minMs*100) / 100
	avgMs = math.Round(avgMs*100) / 100
	maxMs = math.Round(maxMs*100) / 100
	loss = math.Round(loss*10) / 10

	return PingSummary{
		Target:      target,
		Transmitted: transmitted,
		Received:    received,
		LossPercent: loss,
		MinMs:       minMs,
		AvgMs:       avgMs,
		MaxMs:       maxMs,
		Results:     results,
	}
}

func printPingSummary(s PingSummary) {
	fmt.Printf("\n--- %s ping statistics ---\n", s.Target)
	fmt.Printf("%d requests transmitted, %d received, %.1f%% packet loss\n",
		s.Transmitted, s.Received, s.LossPercent)
	if s.Received > 0 {
		fmt.Printf("rtt min/avg/max = %.2f/%.2f/%.2f ms\n", s.MinMs, s.AvgMs, s.MaxMs)
	}
	fmt.Println()
}
