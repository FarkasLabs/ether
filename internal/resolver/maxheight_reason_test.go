package resolver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
)

// The max-height rejection must name whoever actually limited the
// release: the client's caps or the owner's configured profile (#86).
func TestMaxHeightRejectionNamesLimiter(t *testing.T) {
	s := New(&fakeIndexer{}, nil, nil, testCfg(), Options{})
	defer s.Close()
	capsFor := func(maxHeight int) core.ClientCaps {
		return core.ClientCaps{Product: "test", MaxHeight: maxHeight, VideoCodecs: []string{"h264", "hevc"}}
	}
	profileFor := func(raw, client int) core.QualityProfile {
		q := config.Quality{Default: "p", Profiles: []core.QualityProfile{{Name: "p", MaxHeight: raw}}}
		return SelectProfile(q, capsFor(client), nil)
	}
	r := withParsed(rel("uhd", "The.Signal.1999.2160p.WEB-DL.HEVC-GRP", 8000))

	for _, tc := range []struct {
		name      string
		raw       int // configured profile ceiling
		client    int // caps ceiling
		preferred bool
		want      string
	}{
		{"client caps below profile", 2160, 1080, false, "above client max height"},
		{"profile below client caps", 1080, 2160, false, "above profile max height"},
		{"preferred bypasses client limiter", 2160, 1080, true, ""},
		{"preferred bypasses profile limiter", 1080, 2160, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			profile := profileFor(tc.raw, tc.client)
			if profile.ConfiguredMaxHeight != tc.raw {
				t.Fatalf("configured ceiling=%d want %d (profile %+v)", profile.ConfiguredMaxHeight, tc.raw, profile)
			}
			got := s.rejectReason(r, matrixTitle(), searchRequestFor(matrixTitle()), profile, s.resolverCfg(), capsFor(tc.client), nil, tc.preferred)
			if got != tc.want {
				t.Fatalf("reason=%q want %q", got, tc.want)
			}
			// An owner pin never passes through the height gates at all.
			if pin := s.pinRejectReason(r, searchRequestFor(matrixTitle()), capsFor(tc.client), nil); pin != "" {
				t.Fatalf("pin reason=%q want bypass", pin)
			}
		})
	}
}

// A resolve records all three ceilings on its trace and history record,
// and history written before they existed still decodes.
func TestTraceAndHistoryRecordMaxHeightCeilings(t *testing.T) {
	idx := &fakeIndexer{releases: movieReleases()}
	svc := New(idx, fakeHealth{}, nil, testCfg(), Options{})
	defer svc.Close()
	caps := appleTVCaps() // selects the 4K profile (2160 configured)
	caps.MaxHeight = 1080 // ... clamped by this client
	res, err := svc.Resolve(context.Background(), core.ResolveRequest{Title: matrixTitle(), Client: caps})
	if err != nil || res.Release.ID != "hd" {
		t.Fatalf("resolve res=%+v err=%v", res, err)
	}
	traces := svc.Traces()
	if len(traces) == 0 {
		t.Fatal("no trace recorded")
	}
	tr := traces[0]
	if tr.Limits == nil || tr.Limits.ProfileConfiguredMaxHeight != 2160 || tr.Limits.ClientMaxHeight != 1080 || tr.Limits.ProfileMaxHeight != 1080 {
		t.Fatalf("trace limits=%+v, want configured/client/effective 2160/1080/1080", tr.Limits)
	}
	for _, c := range tr.Candidates {
		if c.ReleaseID == "uhd" && c.Reason != "above client max height" {
			t.Fatalf("uhd reason=%q want client limiter", c.Reason)
		}
	}
	records, total, err := svc.History(context.Background(), HistoryFilter{})
	if err != nil || total != 1 || len(records) != 1 {
		t.Fatalf("history records=%v total=%d err=%v", records, total, err)
	}
	if h := records[0]; h.ProfileMaxHeight != 2160 || h.ClientMaxHeight != 1080 || h.EffectiveMaxHeight != 1080 {
		t.Fatalf("history ceilings configured=%d client=%d effective=%d, want 2160/1080/1080",
			h.ProfileMaxHeight, h.ClientMaxHeight, h.EffectiveMaxHeight)
	}
	// Legacy payloads (no ceiling fields) decode with zero values.
	var old Trace
	if err := json.Unmarshal([]byte(`{"profile":"1080p"}`), &old); err != nil {
		t.Fatal(err)
	}
	if h := historyFromTrace(old); h.ProfileMaxHeight != 0 || h.ClientMaxHeight != 0 || h.EffectiveMaxHeight != 0 {
		t.Fatalf("legacy history ceilings=%+v want zero", h)
	}
}
