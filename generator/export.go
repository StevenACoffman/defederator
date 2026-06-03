package generator

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/gqlgo/gqlgenc/clientgenv2"
)

// exportedOperation is the JSON shape written to the export_operations file.
type exportedOperation struct {
	Name         string `json:"name"`
	Operation    string `json:"operation"`
	ResponseType string `json:"responseType"`
}

// exportOperations writes a JSON manifest of all generated operations to path.
// Useful for APQ pre-registration, cost analysis, and non-Go tooling.
func exportOperations(path string, ops []*clientgenv2.Operation) error {
	exported := make([]exportedOperation, len(ops))
	for i, op := range ops {
		exported[i] = exportedOperation{
			Name:         op.Name,
			Operation:    op.Operation,
			ResponseType: op.ResponseStructName,
		}
	}
	b, err := json.MarshalIndent(exported, "", "  ")
	if err != nil {
		return fmt.Errorf("generator: marshal operations manifest: %w", err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("generator: write operations manifest %q: %w", path, err)
	}
	return nil
}
