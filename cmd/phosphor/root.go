package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var (
	cfgFile string
	version = "0.1.0"

	rootCmd = &cobra.Command{
		Use:   "phosphor",
		Short: "Phosphor is an intelligent, resilient terminal-first LLM gateway and routing proxy",
		Long: `Phosphor is a high-performance reverse proxy for LLMs providing:
  * OpenAI-compatible /v1/chat/completions endpoint
  * Intelligent multi-provider routing (Priority, Least-Cost, Lowest-Latency)
  * Resilient failover engine with circuit breakers
  * Pure-Go SQLite telemetry and spend tracking
  * Real-time terminal stats and streaming SSE support`,
		Run: func(cmd *cobra.Command, args []string) {
			_ = cmd.Help()
		},
	}
)

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&cfgFile, "config", "c", "", "config file (default is ./config.yaml or ~/.phosphor/config.yaml)")
	rootCmd.Version = version
}

// GetConfigFile returns the specified config file path if any
func GetConfigFile() string {
	return cfgFile
}

// GetRootCmd returns the root cobra command (useful for testing or sub-commands)
func GetRootCmd() *cobra.Command {
	return rootCmd
}
