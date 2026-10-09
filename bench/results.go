package bench

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// SchemaVersion is results.json's format. It changes when a reader of an
// old file would misread a new one.
const SchemaVersion = 1

// Version is the library's, stamped into results.json.
const Version = "0.1.0"

// Results is the whole run, what results.json holds.
type Results struct {
	Schema int    `json:"schema"`
	Tool   string `json:"tool"`
	// ID names the run: its start time and a short hash. Tags are what
	// the run was tagged with, for `compare --tags`.
	ID       string    `json:"id,omitempty"`
	Tags     []string  `json:"tags,omitempty"`
	Started  time.Time `json:"started"`
	Duration float64   `json:"duration_s"`
	// Title heads the report: the model, when every row runs the same one,
	// else the targets.
	Title   string   `json:"title,omitempty"`
	Machine Machine  `json:"machine"`
	Options Options  `json:"options"`
	Rows    []Row    `json:"rows"`
	Notes   []string `json:"notes,omitempty"`
	// Comparison is set when the results are a comparison of tags rather
	// than a run: which tags, from which runs, and what makes it unfair.
	Comparison *Comparison `json:"comparison,omitempty"`
}

// Comparison describes a Results built by comparing tags.
type Comparison struct {
	Tags     []string `json:"tags"`
	Sources  []string `json:"sources"`            // the run ids compared
	Warnings []string `json:"warnings,omitempty"` // what makes it not like for like
	Missing  []string `json:"missing,omitempty"`  // cells one side did not measure
}

// NewID is a run's id: the start time to the second and a short hash of
// the time and what ran, so two runs started in the same second differ.
func NewID(started time.Time, seed string) string {
	h := sha256.Sum256([]byte(started.Format(time.RFC3339Nano) + seed))
	return started.Format("20060102-150405") + "-" + hex.EncodeToString(h[:3])
}

// Machine is where the bench ran. For a local backend that is also where
// the model ran; for a remote endpoint it is only the client.
type Machine struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
	Chip string `json:"chip,omitempty"`
	CPUs int    `json:"cpus"`
	RAM  uint64 `json:"ram_bytes"`
	// GPU and its memory, when the caller knows them.
	GPU          string `json:"gpu,omitempty"`
	GPUMem       uint64 `json:"gpu_mem_bytes,omitempty"`
	GPUMemSource string `json:"gpu_mem_source,omitempty"`
}

// String is the one-line form for the report's head.
func (m Machine) String() string {
	parts := []string{m.OS + "/" + m.Arch}
	if m.Chip != "" {
		parts = append(parts, m.Chip)
	}
	if m.CPUs > 0 {
		parts = append(parts, fmt.Sprintf("%d CPUs", m.CPUs))
	}
	if m.RAM > 0 {
		parts = append(parts, HumanBytes(m.RAM)+" RAM")
	}
	if m.GPU != "" {
		g := "GPU " + m.GPU
		if m.GPUMem > 0 {
			g += " " + HumanBytes(m.GPUMem)
			if m.GPUMemSource != "" {
				g += " (" + m.GPUMemSource + ")"
			}
		}
		parts = append(parts, g)
	}
	return strings.Join(parts, ", ")
}

// DetectMachine reads the OS, architecture, CPU count and memory, and the
// chip's name where the system says it.
func DetectMachine() Machine {
	m := Machine{OS: runtime.GOOS, Arch: runtime.GOARCH, CPUs: runtime.NumCPU()}
	switch runtime.GOOS {
	case "darwin":
		m.Chip = sysctl("machdep.cpu.brand_string")
		if n, err := strconv.ParseUint(sysctl("hw.memsize"), 10, 64); err == nil {
			m.RAM = n
		}
	case "linux":
		if raw, err := os.ReadFile("/proc/meminfo"); err == nil {
			for _, line := range strings.Split(string(raw), "\n") {
				if f := strings.Fields(line); len(f) >= 2 && f[0] == "MemTotal:" {
					if kb, err := strconv.ParseUint(f[1], 10, 64); err == nil {
						m.RAM = kb * 1024
					}
				}
			}
		}
		if raw, err := os.ReadFile("/proc/cpuinfo"); err == nil {
			for _, line := range strings.Split(string(raw), "\n") {
				if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) == "model name" {
					m.Chip = strings.TrimSpace(v)
					break
				}
			}
		}
	}
	return m
}

func sysctl(key string) string {
	out, err := exec.Command("sysctl", "-n", key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Row is one target × model: what one endpoint did for one model.
type Row struct {
	// Label names the column: the target, or target/model when a target
	// benched several models.
	Label  string `json:"label"`
	Target string `json:"target"`
	URL    string `json:"url"`
	Model  string `json:"model"`
	// Server is the software detected behind the endpoint, with its version
	// where it tells one; Timing says where the speed figures come from.
	Server        string `json:"server,omitempty"`
	ServerVersion string `json:"server_version,omitempty"`
	Timing        string `json:"timing,omitempty"`
	// Tags are the row's own, beside the run's: an orchestrator tags each
	// backend's row backend:<name>. Runs lists the member run ids when the
	// row aggregates several runs in a comparison.
	Tags []string `json:"tags,omitempty"`
	Runs []string `json:"runs,omitempty"`
	// Info is what the caller knows about this row that the report should
	// show: weights, launch profile, load time.
	Info     []KV         `json:"info,omitempty"`
	Started  time.Time    `json:"started"`
	Duration float64      `json:"duration_s"`
	Speed    []SpeedRow   `json:"speed,omitempty"`
	Tools    *ToolsScore  `json:"tools,omitempty"`
	Context  []ContextRow `json:"context,omitempty"`
	Failures []string     `json:"failures,omitempty"`
	Error    string       `json:"error,omitempty"` // the row did not run at all
}

// HasTag says whether the row carries the tag itself or through its run.
func (r Row) HasTag(tag string, runTags []string) bool {
	for _, t := range append(append([]string(nil), runTags...), r.Tags...) {
		if t == tag {
			return true
		}
	}
	return false
}

// KV is one line of a row's details.
type KV struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// SpeedAt is the cell for a context label and concurrency, nil when the
// row has none.
func (r Row) SpeedAt(ctxLabel string, conc int) *SpeedRow {
	for i := range r.Speed {
		if r.Speed[i].Context == ctxLabel && r.Speed[i].Concurrency == conc {
			return &r.Speed[i]
		}
	}
	return nil
}

// ContextFound counts the needles found over those tried.
func (r Row) ContextFound() (found, total int) {
	for _, c := range r.Context {
		if c.Skipped() {
			continue
		}
		total++
		if c.Found {
			found++
		}
	}
	return
}

// AllFailures is every failure of the row, each with where it happened.
func (r Row) AllFailures() []string {
	out := append([]string(nil), r.Failures...)
	for _, s := range r.Speed {
		for _, f := range s.Failures {
			if !strings.HasPrefix(f, "skipped") && !strings.HasPrefix(f, "missing") {
				out = append(out, fmt.Sprintf("speed %s×%d: %s", s.Context, s.Concurrency, f))
			}
		}
	}
	if r.Tools != nil && r.Tools.HTTPErrors > 0 {
		out = append(out, fmt.Sprintf("tools: %d HTTP errors", r.Tools.HTTPErrors))
	}
	for _, c := range r.Context {
		if c.Err != "" && !c.Skipped() {
			out = append(out, fmt.Sprintf("context %s at %d%%: %s", c.Context, c.Depth, c.Err))
		}
	}
	return out
}

// Save writes results.json.
func Save(path string, r Results) error {
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

// Load reads a results.json written by this or an earlier schema it still
// understands.
func Load(path string) (Results, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Results{}, err
	}
	var r Results
	if err := json.Unmarshal(raw, &r); err != nil {
		return r, fmt.Errorf("%s: %w", path, err)
	}
	if r.Schema != SchemaVersion {
		return r, fmt.Errorf("%s: results schema %d; this build reads %d", path, r.Schema, SchemaVersion)
	}
	return r, nil
}

// HumanBytes prints a size the way an operator reads one: "222.7 GiB".
func HumanBytes(n uint64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
