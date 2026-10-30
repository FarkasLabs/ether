package resolver

import (
	"context"
	"encoding/json"
	"github.com/braedonsaunders/ether/internal/core"
	"testing"
)

func TestPreferencePreviewAndPlaybackAgreeWithoutSaving(t *testing.T) {
	f := newConsoleFixture(t)
	defer f.svc.Close()
	p, _, _ := f.svc.consoleProfile("")
	p.ReleaseRules = []core.ReleaseRule{{ID: "group", Name: "Prefer 720", Action: "prefer", Conditions: []core.ReleaseCondition{{Field: "height", Operator: "range", Min: 720, Max: 720}}}}
	body, _ := json.Marshal(map[string]any{"profile": p, "key": matrixTitle().Key.String()})
	var preview struct {
		Releases []ConsoleRelease `json:"releases"`
		Warning  string           `json:"warning"`
	}
	if status := f.do(t, "POST", "/rules/preview", string(body), &preview); status != 200 {
		t.Fatalf("status %d", status)
	}
	rows := preview.Releases
	if len(rows) == 0 || rows[0].Release.Info.Height != 720 || rows[0].Rank != 1 {
		t.Fatalf("preview %+v", rows)
	}
	cfg := testCfg()
	cfg.Quality.Profiles = []core.QualityProfile{p}
	cfg.Quality.Default = p.Name
	svc := New(&fakeIndexer{releases: movieReleases()}, fakeHealth{}, nil, cfg, Options{})
	defer svc.Close()
	actual, err := svc.Resolve(context.Background(), core.ResolveRequest{Title: matrixTitle(), Client: plainCaps()})
	if err != nil {
		t.Fatal(err)
	}
	if actual.Release.ID != rows[0].Release.ID {
		t.Fatalf("preview %s playback %s", rows[0].Release.ID, actual.Release.ID)
	}
	if n := len(f.svc.Pins()); n != 0 {
		t.Fatalf("preview changed pins: %d", n)
	}
	saved, _, _ := f.svc.consoleProfile("")
	if len(saved.ReleaseRules) > 0 {
		t.Fatal("preview saved the draft")
	}
	for _, row := range rows {
		if row.Release.NZBURL != "" || row.Release.MagnetURL != "" || row.Release.StreamURL != "" || row.Release.StreamHeaders != nil {
			t.Fatal("secret-bearing link in preview")
		}
	}
}
func TestPreferencePreviewRejectsInvalidDraftAndMissingLanguage(t *testing.T) {
	f := newConsoleFixture(t)
	defer f.svc.Close()
	p, _, _ := f.svc.consoleProfile("")
	p.ReleaseRules = []core.ReleaseRule{{ID: "lang", Name: "Norwegian", Action: "require", Conditions: []core.ReleaseCondition{{Field: "language", Operator: "equals", Value: "no"}}}}
	body, _ := json.Marshal(map[string]any{"profile": p, "names": []string{"Film.2026.1080p.NORDIC.WEB-DL"}})
	var preview struct {
		Releases []ConsoleRelease `json:"releases"`
	}
	if f.do(t, "POST", "/rules/preview", string(body), &preview) != 200 || len(preview.Releases) != 1 || !preview.Releases[0].Rejected || !preview.Releases[0].Preferences.Unknown {
		t.Fatalf("%+v", preview)
	}
	p.ReleaseRules[0].Conditions[0] = core.ReleaseCondition{Field: "name", Operator: "regex", Value: "(?=x)"}
	body, _ = json.Marshal(map[string]any{"profile": p, "names": []string{"x"}})
	if f.do(t, "POST", "/rules/preview", string(body), nil) != 400 {
		t.Fatal("invalid regex accepted")
	}
}

func TestFilenamePreviewDoesNotInventUsenet(t *testing.T) {
	f := newConsoleFixture(t)
	defer f.svc.Close()
	p, _, _ := f.svc.consoleProfile("")
	p.ReleaseRules = []core.ReleaseRule{{ID: "proto", Name: "Usenet", Action: "require", Conditions: []core.ReleaseCondition{{Field: "protocol", Operator: "equals", Value: "usenet"}}}}
	body, _ := json.Marshal(map[string]any{"profile": p, "names": []string{"Film.2026.1080p.WEB-DL"}})
	var preview struct {
		Releases []ConsoleRelease `json:"releases"`
	}
	if f.do(t, "POST", "/rules/preview", string(body), &preview) != 200 || len(preview.Releases) != 1 {
		t.Fatalf("%+v", preview)
	}
	row := preview.Releases[0]
	if !row.Rejected || row.Preferences == nil || !row.Preferences.Unknown {
		t.Fatalf("filename protocol %+v", row)
	}
	if row.Release.Protocol != "" {
		t.Fatalf("preview protocol %q", row.Release.Protocol)
	}
	if len(row.Preferences.Explanations) != 1 || row.Preferences.Explanations[0].Outcome != "unknown" {
		t.Fatalf("explanations %+v", row.Preferences.Explanations)
	}
}
