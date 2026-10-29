package trackprefs

import (
	"github.com/braedonsaunders/ether/internal/core"
	"testing"
)

func TestConfiguredLanguageOrderBeforeTier(t *testing.T) {
	for _, tt := range []struct {
		want     []string
		tracks   []string
		selected int
	}{
		{[]string{"pt-BR", "en"}, []string{"por", "eng"}, 0},
		{[]string{"pt", "en"}, []string{"pt-BR", "eng"}, 0},
		{[]string{"zh-TW", "en"}, []string{"zh", "eng"}, 0},
		{[]string{"pt-BR", "en"}, []string{"por", "pt-BR", "eng"}, 1},
		{[]string{"en", "pt-BR"}, []string{"por", "eng"}, 1},
	} {
		for _, kind := range []string{"audio", "subtitle"} {
			var streams []core.StreamInfo
			for i, lang := range tt.tracks {
				streams = append(streams, core.StreamInfo{Type: kind, Language: lang, Index: i, Default: i == len(tt.tracks)-1})
			}
			if got := pick(streams, kind, tt.want, "normal", false, nil); got != tt.selected {
				t.Errorf("%s wants %v tracks %v selected %d want %d", kind, tt.want, tt.tracks, got, tt.selected)
			}
		}
	}
}

func TestLanguageOrderPreservesExplicitChoice(t *testing.T) {
	want := make([]string, 21)
	for i := range want {
		want[i] = "pt-BR"
	}
	streams := []core.StreamInfo{{Type: "audio", Language: "pt-BR"}, {Type: "audio", Language: "eng"}}
	if got := pick(streams, "audio", want, "", false, &Choice{Language: "en"}); got != 1 {
		t.Fatalf("explicit English choice lost to language score: %d", got)
	}
}
