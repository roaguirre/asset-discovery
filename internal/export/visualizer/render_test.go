package visualizer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEmbeddedPageRenderer_RenderIncludesViewerHooks(t *testing.T) {
	htmlPath := filepath.Join(t.TempDir(), "visualizer.html")
	renderer := NewEmbeddedPageRenderer()
	ts := time.Date(2026, time.March, 24, 11, 15, 0, 0, time.FixedZone("-0300", -3*60*60))

	runs := []Run{{
		RunSummary: RunSummary{
			ID:               "run-render",
			Label:            "2026-03-24 11:15:00 -0300",
			CreatedAt:        ts,
			AssetCount:       1,
			EnumerationCount: 1,
			SeedCount:        1,
		},
		Rows: []Row{{
			AssetID:           "asset-1",
			Identifier:        "api.example.com",
			AssetType:         "domain",
			DomainKind:        "subdomain",
			RegistrableDomain: "example.com",
			Source:            "crt.sh",
			Status:            "completed",
			TracePath:         "#trace/run-render/asset-1",
			DiscoveryDate:     ts,
		}},
	}}

	if err := renderer.Render(htmlPath, runs, ts); err != nil {
		t.Fatalf("expected render to succeed, got %v", err)
	}

	data, err := os.ReadFile(htmlPath)
	if err != nil {
		t.Fatalf("expected rendered HTML to exist, got %v", err)
	}

	html := string(data)
	assertInlineScriptBalanced(t, html)
	for _, needle := range []string{
		"run-render",
		"api.example.com",
		"source-filter-options",
		"trace-view-button",
		"Judge Analysis",
		`id="app-tooltip"`,
		"showTooltip(",
		"const summaryByKey = new Map();",
		"const allDomainRows = run ? rowsForSourceFilter(run.rows) : [];",
		"const summaryRow = summaryByKey.get(group.key) || null;",
		"const displayRow = summaryRow || group.rows[0] || null;",
		"const summaryIdentifier = escapeHTML(group.key);",
		"function rowsForSourceFilter(runRows)",
		`sources: []`,
		`state.sources.every((source) => rowSources.includes(source))`,
		"#trace/run-render/asset-1",
	} {
		if !strings.Contains(html, needle) {
			t.Fatalf("expected rendered HTML to contain %q", needle)
		}
	}
}

func assertInlineScriptBalanced(t *testing.T, html string) {
	t.Helper()

	start := strings.Index(html, "<script>")
	if start < 0 {
		t.Fatalf("expected rendered HTML to include an inline script")
	}
	start += len("<script>")
	end := strings.Index(html[start:], "</script>")
	if end < 0 {
		t.Fatalf("expected rendered HTML to close the inline script")
	}

	script := html[start : start+end]
	type stackEntry struct {
		token rune
		line  int
	}

	var (
		stack          []stackEntry
		inSingleQuote  bool
		inDoubleQuote  bool
		inTemplate     bool
		inLineComment  bool
		inBlockComment bool
		escaped        bool
		line           = 1
	)

	matching := map[rune]rune{'(': ')', '{': '}', '[': ']'}

	for i, r := range script {
		if r == '\n' {
			line++
			if inLineComment {
				inLineComment = false
			}
		}

		if inLineComment {
			continue
		}
		if inBlockComment {
			if r == '*' && i+1 < len(script) && script[i+1] == '/' {
				inBlockComment = false
			}
			continue
		}
		if inSingleQuote {
			if escaped {
				escaped = false
				continue
			}
			if r == '\\' {
				escaped = true
				continue
			}
			if r == '\'' {
				inSingleQuote = false
			}
			continue
		}
		if inDoubleQuote {
			if escaped {
				escaped = false
				continue
			}
			if r == '\\' {
				escaped = true
				continue
			}
			if r == '"' {
				inDoubleQuote = false
			}
			continue
		}
		if inTemplate {
			if escaped {
				escaped = false
				continue
			}
			if r == '\\' {
				escaped = true
				continue
			}
			if r == '`' {
				inTemplate = false
			}
			continue
		}

		if r == '/' && i+1 < len(script) {
			switch script[i+1] {
			case '/':
				inLineComment = true
				continue
			case '*':
				inBlockComment = true
				continue
			}
		}

		switch r {
		case '\'':
			inSingleQuote = true
		case '"':
			inDoubleQuote = true
		case '`':
			inTemplate = true
		case '(', '{', '[':
			stack = append(stack, stackEntry{token: r, line: line})
		case ')', '}', ']':
			if len(stack) == 0 {
				t.Fatalf("expected balanced inline script, found unexpected %q at line %d", string(r), line)
			}
			top := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if matching[top.token] != r {
				t.Fatalf("expected balanced inline script, %q at line %d was closed by %q at line %d", string(top.token), top.line, string(r), line)
			}
		}
	}

	if len(stack) != 0 {
		top := stack[len(stack)-1]
		t.Fatalf("expected balanced inline script, %q opened at line %d was never closed", string(top.token), top.line)
	}
}
