package bench

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Store is the local results store: every run's results.json, named by its
// id, so runs can be listed, tagged after the fact and compared by tag.
// The tags live in the files; there is no index to fall out of step.
type Store struct {
	Dir string
}

// DefaultStoreDir is $XDG_DATA_HOME/linfer-bench/runs, which is
// ~/.local/share/linfer-bench/runs.
func DefaultStoreDir() string {
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, "linfer-bench", "runs")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", "linfer-bench-runs")
	}
	return filepath.Join(home, ".local", "share", "linfer-bench", "runs")
}

func (s Store) path(id string) string { return filepath.Join(s.Dir, id+".json") }

// Put saves the run under its id and returns the file's path.
func (s Store) Put(r Results) (string, error) {
	if r.ID == "" {
		return "", errors.New("the run has no id")
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return "", err
	}
	return s.path(r.ID), Save(s.path(r.ID), r)
}

// List is every stored run, newest first. A file that does not read is
// reported in the error and the rest are still returned.
func (s Store) List() ([]Results, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var runs []Results
	var errs []error
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		r, err := Load(filepath.Join(s.Dir, e.Name()))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		runs = append(runs, r)
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].Started.After(runs[j].Started) })
	return runs, errors.Join(errs...)
}

// Find is the run with the id, or the one run whose id starts with it.
func (s Store) Find(id string) (Results, error) {
	if r, err := Load(s.path(id)); err == nil {
		return r, nil
	}
	runs, _ := s.List()
	var hits []Results
	for _, r := range runs {
		if strings.HasPrefix(r.ID, id) {
			hits = append(hits, r)
		}
	}
	switch len(hits) {
	case 0:
		return Results{}, fmt.Errorf("no stored run %q (see `linfer-bench runs`)", id)
	case 1:
		return hits[0], nil
	}
	var ids []string
	for _, h := range hits {
		ids = append(ids, h.ID)
	}
	return Results{}, fmt.Errorf("%q matches %d runs: %s", id, len(hits), strings.Join(ids, ", "))
}

// SetTags adds and removes tags on a stored run and saves it.
func (s Store) SetTags(id string, add, remove []string) (Results, error) {
	r, err := s.Find(id)
	if err != nil {
		return r, err
	}
	for _, t := range add {
		if t != "" && !contains(r.Tags, t) {
			r.Tags = append(r.Tags, t)
		}
	}
	var kept []string
	for _, t := range r.Tags {
		if !contains(remove, t) {
			kept = append(kept, t)
		}
	}
	r.Tags = kept
	return r, Save(s.path(r.ID), r)
}

// WithTag is every stored run in which the run or one of its rows carries
// the tag.
func (s Store) WithTag(tag string) ([]Results, error) {
	runs, err := s.List()
	var out []Results
	for _, r := range runs {
		if HasTag(r, tag) {
			out = append(out, r)
		}
	}
	return out, err
}

// HasTag says whether the run or any of its rows carries the tag.
func HasTag(r Results, tag string) bool {
	for _, row := range r.Rows {
		if row.HasTag(tag, r.Tags) {
			return true
		}
	}
	return contains(r.Tags, tag)
}

// AllTags is every tag on the run and its rows, in order of first sight.
func AllTags(r Results) []string {
	out := append([]string(nil), r.Tags...)
	for _, row := range r.Rows {
		for _, t := range row.Tags {
			if !contains(out, t) {
				out = append(out, t)
			}
		}
	}
	return out
}
