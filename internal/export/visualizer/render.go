package visualizer

import (
	"bytes"
	"embed"
	"encoding/json"
	"html/template"
	"os"
	"path/filepath"
	"strings"
	"time"
)

//go:embed assets/template.html assets/visualizer.css assets/visualizer.js
var assetsFS embed.FS

type EmbeddedPageRenderer struct {
	tmpl   *template.Template
	styles string
	script string
}

func NewEmbeddedPageRenderer() *EmbeddedPageRenderer {
	tmpl := template.Must(template.ParseFS(assetsFS, "assets/template.html"))
	styles := mustReadAsset("assets/visualizer.css")
	script := mustReadAsset("assets/visualizer.js")

	return &EmbeddedPageRenderer{
		tmpl:   tmpl,
		styles: styles,
		script: script,
	}
}

func (r *EmbeddedPageRenderer) Render(path string, runs []Run, generatedAt time.Time) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}

	runsJSON, err := json.Marshal(runs)
	if err != nil {
		return err
	}

	var rendered bytes.Buffer
	data := struct {
		GeneratedAt string
	}{
		GeneratedAt: generatedAt.Format("2006-01-02 15:04:05 -0700"),
	}
	if err := r.tmpl.Execute(&rendered, data); err != nil {
		return err
	}

	page := rendered.String()
	page = strings.Replace(page, "<style id=\"visualizer-style-placeholder\"></style>", "<style>"+r.styles+"</style>", 1)
	page = strings.Replace(page, "<script id=\"visualizer-script-placeholder\"></script>", "<script>\n    const runs = "+string(runsJSON)+";\n"+r.script+"\n  </script>", 1)

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = f.WriteString(page)
	return err
}

func mustReadAsset(path string) string {
	data, err := assetsFS.ReadFile(path)
	if err != nil {
		panic(err)
	}

	return string(data)
}
