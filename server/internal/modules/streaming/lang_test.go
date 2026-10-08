package streaming

import (
	"strings"
	"testing"
)

func TestLangCode(t *testing.T) {
	for in, want := range map[string]string{"eng": "en", "EN-us": "en", "rus": "ru", "tur": "tr", "und": "", "": "", "fre": "fr", "xyz": "xyz"} {
		if got := tsLangCode(in); got != want {
			t.Errorf("tsLangCode(%q) = %q, want %q", in, got, want)
		}
	}
}

func audio(lang, title string) tsProbeStream {
	tags := map[string]string{}
	if lang != "" {
		tags["language"] = lang
	}
	if title != "" {
		tags["title"] = title
	}
	return tsProbeStream{CodecType: "audio", CodecName: "eac3", Channels: 6, Tags: tags}
}

func TestWrongAudio(t *testing.T) {
	video := tsProbeStream{CodecType: "video", CodecName: "hevc", Width: 3840, Height: 2160}
	for _, c := range []struct {
		name          string
		tracks        []tsProbeStream
		orig, profile string
		wrong         bool
	}{
		{"russian dub only", []tsProbeStream{audio("rus", "Dub")}, "en", "en", true},
		{"two dubs", []tsProbeStream{audio("rus", "MVO"), audio("ukr", "")}, "en", "tr", true},
		{"dub and original", []tsProbeStream{audio("rus", ""), audio("eng", "")}, "en", "en", false},
		{"untagged track is likely the original", []tsProbeStream{audio("rus", ""), audio("und", "")}, "en", "en", false},
		{"no tags at all", []tsProbeStream{audio("", "")}, "en", "en", false},
		{"title says original", []tsProbeStream{audio("rus", "Original")}, "en", "en", false},
		{"title names the original language", []tsProbeStream{audio("rus", "English 5.1")}, "en", "en", false},
		{"Turkish dub for a Turkish profile", []tsProbeStream{audio("tur", "")}, "en", "tr", false},
		{"Turkish dub for an English profile", []tsProbeStream{audio("tur", "")}, "en", "en", true},
		{"Russian film", []tsProbeStream{audio("rus", "")}, "ru", "en", false},
		{"original language unknown", []tsProbeStream{audio("rus", "")}, "", "en", false},
		{"LANGUAGE tag in upper case", []tsProbeStream{{CodecType: "audio", Tags: map[string]string{"LANGUAGE": "rus"}}}, "en", "en", true},
	} {
		p := probeOf(append([]tsProbeStream{video}, c.tracks...)...)
		if got := tsWrongAudio(p, c.orig, c.profile); (got != "") != c.wrong {
			t.Errorf("%s: got %q, want wrong=%v", c.name, got, c.wrong)
		}
	}
}

func TestLangScore(t *testing.T) {
	for _, c := range []struct {
		name, orig string
		want       float64
	}{
		{"Unabomber 2026 x265 WEB DL 2160p SDR", "en", 0},
		{"Unabomber 2026 WEB-DL 2160p HEVC MVO", "en", tsLangDubPart},
		{"Unabomber.2026.1080p.WEB-DL.Rus.Eng", "en", tsLangMultiPart},
		{"Unabomber 2026 1080p WEB-DL MULTi", "en", tsLangMultiPart},
		{"Unabomber 2026 1080p WEB-DL Dual Audio", "en", tsLangMultiPart},
		{"Unabomber 2026 1080p WEB-DL iTA", "en", tsLangDubPart},
		{"Unabomber 2026 1080p WEB-DL iTA ENG", "en", tsLangMultiPart},
		{"Unabomber (2026) WEB-DL 1080p | Дубляж", "en", tsLangDubPart},
		{"Unabomber 2026 TRUEFRENCH 1080p WEB", "en", tsLangDubPart},
		{"Unabomber 2026 VFF MULTi 1080p WEB", "en", tsLangMultiPart},
		{"Unabomber 2026 1080p Latino", "en", tsLangDubPart},
		{"La Grande Bellezza 2013 1080p BluRay iTA", "it", 0},
		{"Unabomber 2026 1080p WEB-DL Rus", "", tsLangDubPart},
	} {
		title := c.name
		rest := tsNormalize(title)[1:]
		if got := tsLangScore(title, rest, c.orig); got != c.want {
			t.Errorf("%q (orig %q): %v, want %v", c.name, c.orig, got, c.want)
		}
	}
}

// The name part moves a dub-only release below a plain one of the same release
func TestLangPartInScore(t *testing.T) {
	q := tsResolveQuery{mediaType: "movie", title: "Unabomber", year: 2026, hevc: true, origLang: "en"}
	plain, ok1 := tsScore(kr("Unabomber 2026 2160p WEB-DL x265", 50, 8*tsGB, hx('a')), q, tsNormalize(q.title))
	dub, ok2 := tsScore(kr("Unabomber 2026 2160p WEB-DL x265 MVO", 50, 8*tsGB, hx('b')), q, tsNormalize(q.title))
	if !ok1 || !ok2 || plain.score-dub.score != -tsLangDubPart || !strings.Contains(dub.parts.String(), "lang -8.0") {
		t.Fatalf("plain %.1f [%s], dub %.1f [%s]", plain.score, plain.parts, dub.score, dub.parts)
	}
}

// A checked release whose audio is only a dub is unfit: any other good one wins (even a lower
// resolution), but it is still taken when nothing else is left.
func TestProbedLanguageDemotes(t *testing.T) {
	s := NewService(nil, Config{})
	s.probeCache.Store(hx('r')+":0", probeOf(tsProbeStream{CodecType: "video", CodecName: "hevc", Width: 3840, Height: 2160},
		audio("rus", ""), audio("rus", "MVO")))
	s.probeCache.Store(hx('e')+":0", probeOf(tsProbeStream{CodecType: "video", CodecName: "h264", Width: 1920, Height: 1080},
		audio("eng", "")))
	q := tsResolveQuery{mediaType: "movie", title: "Unabomber", origLang: "en", lang: "en"}

	ru := measured("ru", "2160p", 6, 2)
	ru.Hash, ru.rank = hx('r'), 200
	en := measured("en", "1080p", 3, 1)
	en.Hash, en.rank = hx('e'), 150
	if !s.applyProbedLanguage(ru, q) || ru.unfit == "" {
		t.Fatal("Russian-only file not marked")
	}
	if s.applyProbedLanguage(en, q) || en.unfit != "" {
		t.Fatalf("English file marked: %q", en.unfit)
	}
	if i, _ := tsChoose([]*tsResolveResult{ru, en}, []bool{true, true}); i != 1 {
		t.Fatalf("got %d, want the release with the original audio", i)
	}
	if i, final := tsChoose([]*tsResolveResult{ru, nil}, []bool{true, true}); i != 0 || !final {
		t.Fatalf("alone: got %d final=%v, the only source must still play", i, final)
	}
	if i := tsChoosePlayable([]*tsResolveResult{ru, en}); i != 1 {
		t.Fatalf("playable: got %d", i)
	}

	// A Turkish profile accepts a Turkish dub
	s.probeCache.Store(hx('t')+":0", probeOf(audio("tur", "")))
	tr := measured("tr", "1080p", 3, 1)
	tr.Hash = hx('t')
	q.lang = "tr"
	if s.applyProbedLanguage(tr, q) {
		t.Fatal("Turkish dub marked for a Turkish profile")
	}
}
