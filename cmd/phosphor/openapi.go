package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"

	"github.com/Sriram-Nambiar/Phosphor/internal/proxy"
	"github.com/spf13/cobra"
)

var (
	openapiOutput string
	openapiPretty bool

	openapiCmd = &cobra.Command{
		Use:   "openapi",
		Short: "Print or export OpenAPI 3.1.0 specification for Phosphor gateway",
		RunE:  runOpenAPI,
	}
)

func init() {
	openapiCmd.Flags().StringVarP(&openapiOutput, "output", "o", "", "Filepath to save openapi.json (prints to stdout if omitted)")
	openapiCmd.Flags().BoolVarP(&openapiPretty, "pretty", "p", true, "Pretty-print formatted JSON output")
	rootCmd.AddCommand(openapiCmd)
}

func runOpenAPI(cmd *cobra.Command, args []string) error {
	raw := proxy.OpenAPISpec()
	out := raw
	if openapiPretty {
		var buf bytes.Buffer
		if err := json.Indent(&buf, []byte(raw), "", "  "); err == nil {
			out = buf.String()
		}
	}

	if openapiOutput != "" {
		if err := os.WriteFile(openapiOutput, []byte(out), 0644); err != nil {
			return fmt.Errorf("failed to write openapi spec to %s: %w", openapiOutput, err)
		}
		fmt.Printf("OpenAPI specification exported to %s\n", openapiOutput)
		return nil
	}

	fmt.Println(out)
	return nil
}
