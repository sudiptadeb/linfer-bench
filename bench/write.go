package bench

import (
	"os"
	"path/filepath"
	"time"
)

// DefaultOut is the report directory when none is given:
// ./linfer-bench-<timestamp>.
func DefaultOut(now time.Time) string {
	return "linfer-bench-" + now.Format("2006-01-02-150405")
}

// WriteFiles writes results.json (comparison.json for a comparison of
// tags), report.md and report.html into dir, creating it, and returns
// their paths.
func WriteFiles(dir string, r Results) ([]string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	name := "results.json"
	if r.Comparison != nil {
		name = "comparison.json"
	}
	files := []string{filepath.Join(dir, name), filepath.Join(dir, "report.md"), filepath.Join(dir, "report.html")}
	if err := Save(files[0], r); err != nil {
		return nil, err
	}
	if err := os.WriteFile(files[1], []byte(Markdown(r)), 0o644); err != nil {
		return nil, err
	}
	html, err := HTML(r)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(files[2], []byte(html), 0o644); err != nil {
		return nil, err
	}
	return files, nil
}
