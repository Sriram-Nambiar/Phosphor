package main

import (
	"fmt"
	"runtime"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

var (
	gitCommit = "dev"
	buildDate = "unknown"

	versionCmd = &cobra.Command{
		Use:   "version",
		Short: "Print Phosphor version, Go runtime, and build metadata",
		Run: func(cmd *cobra.Command, args []string) {
			bold := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205"))
			dim := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
			fmt.Printf("%s %s (%s/%s)\n", bold.Render("Phosphor"), version, runtime.GOOS, runtime.GOARCH)
			fmt.Printf("%s %s\n", dim.Render("Go Runtime:"), runtime.Version())
			if gitCommit != "" && gitCommit != "dev" {
				fmt.Printf("%s %s\n", dim.Render("Git Commit:"), gitCommit)
			}
			if buildDate != "" && buildDate != "unknown" {
				fmt.Printf("%s %s\n", dim.Render("Build Date:"), buildDate)
			}
		},
	}
)

func init() {
	rootCmd.AddCommand(versionCmd)
}
