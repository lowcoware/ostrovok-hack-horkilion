package dataset

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testSnapshot = "testdata/snapshot"

func TestLoadTestSnapshot(t *testing.T) {
	d, err := Load(testSnapshot)
	if err != nil {
		t.Fatalf("test snapshot must load: %v", err)
	}
	if len(d.Destinations) == 0 || len(d.Hotels) == 0 || len(d.Routes) == 0 {
		t.Fatalf("test snapshot is empty: %d destinations, %d hotels, %d routes", len(d.Destinations), len(d.Hotels), len(d.Routes))
	}
	if v, ok := d.Visa("RU", "TR"); !ok || v.Status != "visa_free" {
		t.Errorf("RU passport to Turkey must be visa free, got %+v", v)
	}
	if v, ok := d.Visa("RU", "FR"); !ok || v.Status != "visa_required" {
		t.Errorf("RU passport to France must need a visa, got %+v", v)
	}
	for _, x := range d.Destinations {
		months := d.ClimateByDest[x.ID]
		for m, c := range months {
			if c == nil {
				t.Errorf("destination %s has no climate for month %d", x.ID, m+1)
			}
		}
	}
}

// copySnapshot copies the test snapshot into a temp dir to break it there.
func copySnapshot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	entries, err := os.ReadDir(testSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(testSnapshot, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, e.Name()), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestLoadRejectsBrokenSnapshots(t *testing.T) {
	tests := []struct {
		name   string
		break_ func(t *testing.T, dir string)
		want   string
	}{
		{"missing dataset in manifest", func(t *testing.T, dir string) {
			editManifest(t, dir, func(m *Manifest) {
				var kept []DatasetInfo
				for _, d := range m.Datasets {
					if d.Name != "hotels" {
						kept = append(kept, d)
					}
				}
				m.Datasets = kept
			})
		}, `"hotels" is not in the manifest`},
		{"file changed after the build", func(t *testing.T, dir string) {
			f, err := os.OpenFile(filepath.Join(dir, "countries.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			_, _ = f.WriteString(`{"code":"XX","name_ru":"Х","name_en":"X"}` + "\n")
		}, "sha256"},
		{"missing file", func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, "routes.jsonl")); err != nil {
				t.Fatal(err)
			}
		}, `dataset "routes"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := copySnapshot(t)
			tt.break_(t, dir)
			_, err := Load(dir)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("want error containing %q, got %v", tt.want, err)
			}
		})
	}
}

func editManifest(t *testing.T, dir string, edit func(*Manifest)) {
	t.Helper()
	path := filepath.Join(dir, "manifest.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	edit(&m)
	raw, _ = json.Marshal(m)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}
