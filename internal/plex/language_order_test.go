package plex

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
		info := core.MediaInfo{}
		for i, lang := range tt.tracks {
			info.Streams = append(info.Streams, core.StreamInfo{Type: "audio", Language: lang, Index: i, Default: i == len(tt.tracks)-1})
		}
		if got := pickAudio(info, tt.want); got != tt.selected {
			t.Errorf("wants %v tracks %v selected %d want %d", tt.want, tt.tracks, got, tt.selected)
		}
	}
}
