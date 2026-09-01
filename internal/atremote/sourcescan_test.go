package atremote

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// atCommandLiteral matches a Go string literal that looks like an AT command or
// an AT response keyword. Command selection belongs to internal/modemadapter;
// a transport that contains one has started owning model behavior.
var atCommandLiteral = regexp.MustCompile(`(?i)^(at([+&#*$^=?]|[a-z]{0,2}[0-9=?;]|[a-z]?$)|\+c[a-z]{2,}|\+m[a-z]{2,}|ok$|error$)`)

func repositoryRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatalf("resolve working directory: %v", err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(directory, "go.mod")); statErr == nil {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatal("repository root with go.mod was not found")
		}
		directory = parent
	}
}

func TestTransportContainsNoATCommandLiteral(t *testing.T) {
	// Self-check: a vacuous matcher would make the scan below meaningless.
	for _, positive := range []string{"AT", "AT+CPIN?", "AT+CGDCONT=1,\\\"IP\\\"", "ATE0", "ATH", "ATD10086;", "+CME ERROR:", "+MCCID", "OK", "ERROR"} {
		if !atCommandLiteral.MatchString(positive) {
			t.Fatalf("matcher does not recognize AT literal %q", positive)
		}
	}
	for _, negative := range []string{"application/json", "/at/session", "at-bridge:", "session", "bridges", "atremote", "attransport", "attest"} {
		if atCommandLiteral.MatchString(negative) {
			t.Fatalf("matcher misclassifies %q as an AT literal", negative)
		}
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package directory: %v", err)
	}
	fileSet := token.NewFileSet()
	inspected := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		inspected++
		file, parseErr := parser.ParseFile(fileSet, name, nil, parser.SkipObjectResolution)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", name, parseErr)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			literal, ok := node.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			value := strings.Trim(literal.Value, "`\"")
			if atCommandLiteral.MatchString(strings.TrimSpace(value)) {
				t.Errorf("%s:%d contains AT command literal %q; commands belong to internal/modemadapter",
					name, fileSet.Position(literal.Pos()).Line, value)
			}
			return true
		})
	}
	if inspected == 0 {
		t.Fatal("no transport source files were inspected")
	}
}

// TestEndpointSchemeStaysInsideTheTransportBoundary proves the invariant that no
// layer above the transport can tell a bridged control endpoint from a local
// one. If this fails, some package has started branching on transport shape and
// the adapter/transport contract is leaking.
func TestEndpointSchemeStaysInsideTheTransportBoundary(t *testing.T) {
	root := repositoryRoot(t)
	allowed := []string{
		filepath.Join("internal", "atremote"),
		filepath.Join("cmd", "simplus-agent"),
		filepath.Join("docs"),
		filepath.Join(".trellis"),
	}
	scanned := 0
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "node_modules", "web", "dist", "third_party", "ref", ".pio":
				return filepath.SkipDir
			}
			return nil
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if !strings.HasSuffix(relative, ".go") && !strings.HasSuffix(relative, ".yaml") && !strings.HasSuffix(relative, ".md") {
			return nil
		}
		for _, prefix := range allowed {
			if strings.HasPrefix(relative, prefix) {
				return nil
			}
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		scanned++
		if strings.Contains(string(body), EndpointScheme) {
			t.Errorf("%s references the bridge endpoint scheme; only the transport and its composition root may", relative)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan repository: %v", err)
	}
	if scanned == 0 {
		t.Fatal("no files outside the transport boundary were scanned")
	}
}
