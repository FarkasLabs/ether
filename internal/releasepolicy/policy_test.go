package releasepolicy

import (
	"encoding/json"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/releaseparse"
)

func languageRule() core.ReleaseRule {
	return core.ReleaseRule{ID: "audio", Name: "Norwegian", Action: "require", Conditions: []core.ReleaseCondition{{Field: "language", Operator: "equals", Value: "no"}}}
}
func evaluate(t *testing.T, rules []core.ReleaseRule, rel core.Release) Result {
	t.Helper()
	p, err := Compile(rules)
	if err != nil {
		t.Fatal(err)
	}
	return p.Evaluate(rel)
}
func TestLanguageEvidenceAndExplicitFallback(t *testing.T) {
	r := languageRule()
	for _, tc := range []struct {
		name            string
		info            core.ReleaseInfo
		reject, unknown bool
	}{
		{"Nordic is not Norwegian audio", core.ReleaseInfo{}, true, true},
		{"subtitles are not audio", core.ReleaseInfo{SubtitleLanguages: []string{"norwegian"}}, true, true},
		{"multi does not identify language", core.ReleaseInfo{Languages: []string{"multi"}}, true, true},
		{"Norwegian declaration", core.ReleaseInfo{Languages: []string{"norwegian"}}, false, false},
		{"different language", core.ReleaseInfo{Languages: []string{"english"}}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := evaluate(t, []core.ReleaseRule{r}, core.Release{Name: "Film.NORDIC", Info: tc.info})
			if got.Rejected != tc.reject || got.Unknown != tc.unknown {
				t.Fatalf("%+v", got)
			}
		})
	}
	r.Fallback = []core.ReleaseCondition{{Field: "language", Operator: "equals", Value: "en"}}
	english := evaluate(t, []core.ReleaseRule{r}, core.Release{Info: core.ReleaseInfo{Languages: []string{"english"}}})
	norwegian := evaluate(t, []core.ReleaseRule{r}, core.Release{Info: core.ReleaseInfo{Languages: []string{"norwegian"}}})
	if english.Rejected || english.Fallback != 1 || !Better(norwegian, english) {
		t.Fatalf("primary=%+v fallback=%+v", norwegian, english)
	}
	if got := evaluate(t, []core.ReleaseRule{r}, core.Release{}); !got.Rejected {
		t.Fatal("fallback accepted unknown audio")
	}
}
func TestRegionsAreNotInferredFromGenericLanguage(t *testing.T) {
	r := languageRule()
	r.Conditions[0].Value = "pt-BR"
	for _, tc := range []struct {
		lang            string
		reject, unknown bool
	}{
		{"portuguese", true, true},
		{"portuguese-portugal", true, false},
		{"portuguese-brazil", false, false},
	} {
		got := evaluate(t, []core.ReleaseRule{r}, core.Release{Info: core.ReleaseInfo{Languages: []string{tc.lang}}})
		if got.Rejected != tc.reject || got.Unknown != tc.unknown {
			t.Fatalf("%s: %+v", tc.lang, got)
		}
	}
}
func TestConflictingRequirementsAndNumericPrecision(t *testing.T) {
	hevc := core.ReleaseRule{ID: "h", Name: "HEVC", Action: "require", Conditions: []core.ReleaseCondition{{Field: "video", Operator: "equals", Value: "hevc"}}}
	h264 := core.ReleaseRule{ID: "a", Name: "H.264", Action: "require", Conditions: []core.ReleaseCondition{{Field: "video", Operator: "equals", Value: "h264"}}}
	if Conflict([]core.ReleaseRule{hevc, h264}) == "" {
		t.Fatal("conflicting video requirements accepted")
	}
	hevc.Fallback = []core.ReleaseCondition{{Field: "video", Operator: "equals", Value: "h264"}}
	if Conflict([]core.ReleaseRule{hevc, h264}) != "" {
		t.Fatal("a fallback was treated as an impossible requirement")
	}
	raw := []byte(`[{"id":"s","name":"Size","action":"prefer","conditions":[{"field":"size_gib","operator":"range","min":1.25,"max":2.5}]}]`)
	var rules []core.ReleaseRule
	if err := json.Unmarshal(raw, &rules); err != nil {
		t.Fatal(err)
	}
	if rules[0].Conditions[0].Min != 1.25 || rules[0].Conditions[0].Max != 2.5 {
		t.Fatalf("precision lost: %+v", rules[0].Conditions[0])
	}
	size := int64(1.25 * float64(int64(1)<<30))
	got := evaluate(t, rules, core.Release{Size: size})
	if got.Preferences[0] != 1 {
		t.Fatalf("exact bound missed: %+v", got)
	}
}
func TestCorFaceExpressions(t *testing.T) {
	expressions := []string{`\b(NORDIC|SCANDiNAViAN|MULTI[\s._-]?(NORDIC|4|5)|(NOR|SWE|DAN|FIN)[\s._-]?(NOR|SWE|DAN|FIN))\b`, `\b(Nordic)\b`, `(?i)\b(NOR(WEGIAN|SK)?|MULTi[\s._-]?(NOR|4|NORDIC)|NORSK[\s._-]TALE|NO[\s._-]DUB(BED)?|NOR[\s._-]DUB(BED)?)\b`}
	names := []string{"Movie.2026.1080p.SWE.NOR.WEB-DL", "Movie.2026.1080p.NORDIC.WEB-DL", "Movie.2026.1080p.ENG.NOR.WEB-DL"}
	for i, exp := range expressions {
		r := core.ReleaseRule{ID: "nordic", Name: "Nordic", Action: "prefer", Conditions: []core.ReleaseCondition{{Field: "name", Operator: "regex", Value: exp}}}
		got := evaluate(t, []core.ReleaseRule{r}, core.Release{Name: names[i], Info: releaseparse.Parse(names[i])})
		if got.Preferences[0] != 1 {
			t.Fatalf("%s: %+v", names[i], got)
		}
	}
}
func TestPriorityExceptionsAndUnknownLogic(t *testing.T) {
	a := core.ReleaseRule{ID: "a", Name: "Group", Action: "prefer", Conditions: []core.ReleaseCondition{{Field: "group", Operator: "equals", Value: "A"}}}
	b := core.ReleaseRule{ID: "b", Name: "HEVC", Action: "prefer", Conditions: []core.ReleaseCondition{{Field: "video", Operator: "equals", Value: "hevc"}}}
	ra := core.Release{Info: core.ReleaseInfo{Group: "A", VideoCodec: "h264"}}
	rb := core.Release{Info: core.ReleaseInfo{Group: "B", VideoCodec: "hevc"}}
	if !Better(evaluate(t, []core.ReleaseRule{a, b}, ra), evaluate(t, []core.ReleaseRule{a, b}, rb)) {
		t.Fatal("first priority lost")
	}
	if !Better(evaluate(t, []core.ReleaseRule{b, a}, rb), evaluate(t, []core.ReleaseRule{b, a}, ra)) {
		t.Fatal("reorder ignored")
	}
	r := languageRule()
	r.Unless = []core.ReleaseCondition{{Field: "name", Operator: "contains", Value: "TRAILER"}}
	if evaluate(t, []core.ReleaseRule{r}, core.Release{Name: "Film.Trailer"}).Rejected {
		t.Fatal("exception not applied")
	}
	r.Unless = nil
	r.Match = "any"
	r.Conditions = append(r.Conditions, core.ReleaseCondition{Field: "name", Operator: "contains", Value: "NORDIC"})
	if evaluate(t, []core.ReleaseRule{r}, core.Release{Name: "Film.NORDIC"}).Rejected {
		t.Fatal("true OR unknown should pass")
	}
	r.Match = "all"
	if !evaluate(t, []core.ReleaseRule{r}, core.Release{Name: "Film.NORDIC"}).Unknown {
		t.Fatal("true AND unknown should remain unknown")
	}
	r = languageRule()
	r.Action = "avoid"
	if !evaluate(t, []core.ReleaseRule{r}, core.Release{}).Rejected {
		t.Fatal("unknown bypassed exclusion")
	}
	r.Disabled = true
	if evaluate(t, []core.ReleaseRule{r}, core.Release{}).Rejected {
		t.Fatal("disabled rule applied")
	}
}
func TestInvalidPoliciesFailClosed(t *testing.T) {
	for _, c := range []core.ReleaseCondition{{Field: "name", Operator: "regex", Value: "(?=bad)"}, {Field: "height", Operator: "range", Min: 2160, Max: 1080}, {Field: "invalid", Operator: "equals", Value: "x"}, {Field: "language", Operator: "regex", Value: ".*"}} {
		r := languageRule()
		r.Conditions = []core.ReleaseCondition{c}
		if _, err := Compile([]core.ReleaseRule{r}); err == nil {
			t.Fatalf("accepted %+v", c)
		}
	}
	r := languageRule()
	if _, err := Compile([]core.ReleaseRule{r, r}); err == nil {
		t.Fatal("duplicate ID accepted")
	}
}

func TestRegionalWantsDoNotInventCollapsedEvidence(t *testing.T) {
	require := func(value string) core.ReleaseRule {
		return core.ReleaseRule{ID: "lang", Name: "Region", Action: "require", Conditions: []core.ReleaseCondition{{Field: "language", Operator: "equals", Value: value}}}
	}
	rel := func(lang string) core.Release {
		return core.Release{Info: core.ReleaseInfo{Languages: []string{lang}}}
	}
	// A generic want still accepts every declaration of that language,
	// including a tag whose region the parser discarded.
	if got := evaluate(t, []core.ReleaseRule{require("de")}, rel("de-AT")); got.Rejected || got.Unknown {
		t.Fatalf("generic German rejected a German declaration: %+v", got)
	}
	for _, tc := range []struct {
		want, have      string
		reject, unknown bool
	}{
		{"de-DE", "german", true, true},
		{"de-DE", "de", true, true},
		{"de-DE", "de-AT", true, false},
		{"de-DE", "de-DE", false, false},
		{"de-DE", "deu-DE", false, false},
		{"zh-Hant", "chinese", true, true},
		{"zh-Hant", "zh", true, true},
		{"zh-Hant", "zh-Hans", true, false},
		{"zh-Hant", "zh-CN", true, false},
		{"zh-Hant", "zh-Hant", false, false},
		{"zh-Hant", "zh-TW", false, false},
		{"zh-Hant", "zh-Hant-TW", false, false},
		{"pt-BR", "portuguese", true, true},
		{"pt-BR", "portuguese-portugal", true, false},
		{"pt-BR", "portuguese-brazil", false, false},
	} {
		got := evaluate(t, []core.ReleaseRule{require(tc.want)}, rel(tc.have))
		if got.Rejected != tc.reject || got.Unknown != tc.unknown {
			t.Fatalf("want %s have %s: %+v", tc.want, tc.have, got)
		}
	}
}

func TestScriptAndRegionStaySeparate(t *testing.T) {
	require := func(value string) core.ReleaseRule {
		return core.ReleaseRule{ID: "lang", Name: "Script", Action: "require", Conditions: []core.ReleaseCondition{{Field: "language", Operator: "equals", Value: value}}}
	}
	rel := func(lang string) core.Release {
		return core.Release{Info: core.ReleaseInfo{Languages: []string{lang}}}
	}
	for _, tc := range []struct {
		want, have      string
		reject, unknown bool
	}{
		{"sr-Latn", "sr-Latn-RS", false, false},
		{"sr-Latn", "sr-Cyrl-RS", true, false},
		{"sr-Latn", "sr", true, true},
		{"sr-Latn", "sr-RS", true, true},
		{"sr-Latn-RS", "sr-Latn-RS", false, false},
		{"sr-Latn-RS", "sr-Cyrl-RS", true, false},
		{"sr-Latn-RS", "sr-Latn", true, true},
		{"sr-Latn-RS", "sr-Latn-BA", true, false},
		{"sr-RS", "sr-Latn-RS", false, false},
		{"zh-Hant", "zh-Hant-CN", false, false},
		{"zh-Hant-CN", "zh-CN", true, false},
		{"zh-Hant", "zh-HK", true, true},
		{"zh-TW", "zh-Hant", true, true},
		{"zh-CN", "zh-Hans", true, true},
	} {
		got := evaluate(t, []core.ReleaseRule{require(tc.want)}, rel(tc.have))
		if got.Rejected != tc.reject || got.Unknown != tc.unknown {
			t.Fatalf("want %s have %s: %+v", tc.want, tc.have, got)
		}
	}
}

func TestEmptyProtocolIsUsenetAndUnstatedIsUnknown(t *testing.T) {
	rule := core.ReleaseRule{ID: "p", Name: "Usenet", Action: "require", Conditions: []core.ReleaseCondition{{Field: "protocol", Operator: "equals", Value: "usenet"}}}
	live := evaluate(t, []core.ReleaseRule{rule}, core.Release{})
	if live.Rejected || live.Unknown {
		t.Fatalf("empty live protocol is Usenet: %+v", live)
	}
	preview := evaluate(t, []core.ReleaseRule{rule}, core.Release{Protocol: core.ProtocolUnstated})
	if !preview.Rejected || !preview.Unknown {
		t.Fatalf("unstated protocol fabricated Usenet: %+v", preview)
	}
}
