package visualizer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
)

type ArchiveStore interface {
	Save(dir string, run Run) error
	Load(dir string) ([]Run, error)
}

type FileArchiveStore struct{}

func NewFileArchiveStore() *FileArchiveStore {
	return &FileArchiveStore{}
}

func (s *FileArchiveStore) Save(dir string, run Run) error {
	runsDir := filepath.Join(dir, "runs")
	if err := os.MkdirAll(runsDir, 0755); err != nil {
		return err
	}

	snapshotPath := filepath.Join(runsDir, run.ID+".json")
	runCopy := run
	runCopy.DataPath = filepath.ToSlash(mustRelativePath(dir, snapshotPath))
	if err := writeJSONFile(snapshotPath, runCopy); err != nil {
		return err
	}

	manifestPath := filepath.Join(dir, "manifest.json")
	manifest, err := readManifest(manifestPath)
	if err != nil {
		return err
	}

	manifest = upsertRun(manifest, runCopy.RunSummary)
	return writeJSONFile(manifestPath, manifest)
}

func (s *FileArchiveStore) Load(dir string) ([]Run, error) {
	manifestPath := filepath.Join(dir, "manifest.json")
	manifest, err := readManifest(manifestPath)
	if err != nil {
		return nil, err
	}

	runs := make([]Run, 0, len(manifest.Runs))
	for _, summary := range manifest.Runs {
		path := filepath.Join(dir, filepath.FromSlash(summary.DataPath))
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}

		var run Run
		if err := json.Unmarshal(data, &run); err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}

	sort.SliceStable(runs, func(i, j int) bool {
		return runs[i].CreatedAt.After(runs[j].CreatedAt)
	})

	return runs, nil
}

func readManifest(path string) (Manifest, error) {
	var manifest Manifest

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return manifest, nil
		}
		return manifest, err
	}

	if err := json.Unmarshal(data, &manifest); err != nil {
		return manifest, err
	}

	return manifest, nil
}

func upsertRun(manifest Manifest, summary RunSummary) Manifest {
	replaced := false
	for i := range manifest.Runs {
		if manifest.Runs[i].ID == summary.ID {
			manifest.Runs[i] = summary
			replaced = true
			break
		}
	}

	if !replaced {
		manifest.Runs = append(manifest.Runs, summary)
	}

	sort.SliceStable(manifest.Runs, func(i, j int) bool {
		return manifest.Runs[i].CreatedAt.After(manifest.Runs[j].CreatedAt)
	})

	return manifest
}

func writeJSONFile(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	encoder := json.NewEncoder(f)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func mustRelativePath(fromDir, to string) string {
	rel, err := filepath.Rel(fromDir, to)
	if err != nil {
		return filepath.ToSlash(to)
	}

	return rel
}
