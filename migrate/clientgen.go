package migrate

import (
	"bytes"
	_ "embed"
	"fmt"
	"path/filepath"
	"strings"
	"text/template"
	"unicode"
)

//go:embed client.gotpl
var clientTemplate string

// Data holds all values the client.gotpl template needs.
type Data struct {
	ServiceName     string          // e.g. "ai-guide"
	ServiceDir      string          // absolute or relative path passed to migrate
	PackageName     string          // always "cross_service"
	ImportAlias     string          // always "defed"
	DefedImportPath string          // e.g. "github.com/Khan/webapp/services/ai-guide/generated/defederator"
	URLFuncName     string          // e.g. "aiGuideSubgraphURLs"
	Subgraphs       []SubgraphEntry // all join__Graph entries in declaration order
}

// Render executes the embedded client.gotpl template and returns Go source. Pure.
func Render(d Data) (string, error) {
	tmpl, err := template.New("client").Parse(clientTemplate)
	if err != nil {
		return "", fmt.Errorf("migrate: parse client template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, d); err != nil {
		return "", fmt.Errorf("migrate: render client template: %w", err)
	}
	return buf.String(), nil
}

// DataFromDir derives template Data from the service directory path, the webapp
// module path (from go.mod), and the parsed subgraphs. Pure function — no I/O.
func DataFromDir(dir string, modulePath string, subgraphs []SubgraphEntry) Data {
	serviceName := filepath.Base(dir)
	return Data{
		ServiceName:     serviceName,
		ServiceDir:      dir,
		PackageName:     "cross_service",
		ImportAlias:     "defed",
		DefedImportPath: modulePath + "/services/" + serviceName + "/generated/defederator",
		URLFuncName:     urlFuncName(serviceName),
		Subgraphs:       subgraphs,
	}
}

// urlFuncName converts a service name to a Go identifier for the URL function.
// "ai-guide" → "aiGuideSubgraphURLs"
func urlFuncName(serviceName string) string {
	return toCamelCase(serviceName) + "SubgraphURLs"
}

// toCamelCase converts a hyphenated name to lowerCamelCase.
// "ai-guide" → "aiGuide", "content-editing" → "contentEditing"
func toCamelCase(s string) string {
	parts := strings.Split(s, "-")
	var b strings.Builder
	for i, p := range parts {
		if p == "" {
			continue
		}
		if i == 0 {
			b.WriteString(strings.ToLower(p))
		} else {
			runes := []rune(p)
			runes[0] = unicode.ToUpper(runes[0])
			b.WriteString(string(runes))
		}
	}
	return b.String()
}
