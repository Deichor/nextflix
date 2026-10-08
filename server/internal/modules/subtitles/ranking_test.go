package subtitles

import "testing"

func TestParseRelease(t *testing.T) {
	cases := []struct {
		name, family, source, res, group, service string
	}{
		{"Inception.2010.1080p.BluRay.x264-SPARKS", "bluray", "bluray", "1080p", "sparks", ""},
		{"The.Bear.S03E01.1080p.DSNP.WEB-DL.DDP5.1.H.264-FLUX", "web", "webdl", "1080p", "flux", "dsnp"},
		{"Movie.2024.2160p.NF.WEBRip.x265-[YTS.MX]", "web", "webrip", "2160p", "yts", "nf"},
		{"Movie (2024) [1080p] [WEBRip] [YTS.MX]", "web", "webrip", "1080p", "yts", ""},
		{"Show.S01E02.720p.HDTV.x264-KILLERS.srt", "tv", "hdtv", "720p", "killers", ""},
		{"Movie.2024.1080p.WEB-DL", "web", "webdl", "1080p", "", ""},
		{"BDRip-HDRINVASION", "bluray", "bdrip", "", "hdrinvasion", ""},
	}
	for _, c := range cases {
		ri := parseRelease(c.name)
		if ri.family != c.family || ri.source != c.source || ri.res != c.res || ri.group != c.group || ri.service != c.service {
			t.Errorf("%s: got family=%s source=%s res=%s group=%s service=%s", c.name, ri.family, ri.source, ri.res, ri.group, ri.service)
		}
	}
}

func TestRankingOrder(t *testing.T) {
	q := Query{Langs: []string{"tr", "en"}, Title: "Inception.2010.1080p.BluRay.x264-SPARKS"}
	mk := func(id, lang, release string, mod func(*candidate)) candidate {
		c := candidate{Sub: Sub{ID: id, Source: SourceOpenSubtitles, Language: lang, Release: release}, Releases: []string{release}}
		if mod != nil {
			mod(&c)
		}
		return c
	}
	cands := []candidate{
		mk("en-best", "en", "Inception.2010.1080p.BluRay.x264-SPARKS", nil),
		mk("tr-web", "tr", "Inception.2010.1080p.WEB-DL.DD5.1.H264-FGT", func(c *candidate) { c.Downloads = 50000 }),
		mk("tr-machine", "tr", "Inception.2010.1080p.BluRay.x264-SPARKS", func(c *candidate) { c.Machine = true; c.Downloads = 900000 }),
		mk("tr-group", "tr", "Inception.2010.720p.BluRay.x264-SPARKS", nil),
		mk("tr-hash", "tr", "Inception.DVDRip.XviD", func(c *candidate) { c.Synced = true }),
		mk("tr-hi", "tr", "Inception.2010.1080p.BluRay.x264-SPARKS", func(c *candidate) { c.HI = true }),
		mk("tr-forced", "tr", "Inception.2010.1080p.BluRay.x264-SPARKS", func(c *candidate) { c.Forced = true }),
		mk("de", "de", "Inception.2010.1080p.BluRay.x264-SPARKS", nil),
		mk("en-ai", "en", "whatever", func(c *candidate) { c.Machine = true }),
	}
	got := rankCandidates(cands, q)
	want := []string{"tr-hash", "tr-group", "tr-hi", "tr-web", "tr-forced", "tr-machine", "en-best", "en-ai", "de"}
	if len(got) != len(want) {
		t.Fatalf("got %d items", len(got))
	}
	for i, w := range want {
		if got[i].ID != w {
			ids := make([]string, len(got))
			for j, g := range got {
				ids[j] = g.ID
			}
			t.Fatalf("position %d: got %v, want %v", i, ids, want)
		}
	}
	for _, s := range got {
		if s.ID == "tr-machine" && s.Score > 0.1 {
			t.Errorf("machine translation scored %v", s.Score)
		}
		if s.Label == "" || s.Score < 0 || s.Score > 1 {
			t.Errorf("%s: label %q score %v", s.ID, s.Label, s.Score)
		}
	}
	if got[0].Label != "Türkçe" {
		t.Errorf("label %q", got[0].Label)
	}
}

func TestHIOnlyNotPenalised(t *testing.T) {
	q := Query{Langs: []string{"tr"}}
	a := rankCandidates([]candidate{{Sub: Sub{ID: "a", Language: "tr", HI: true}}}, q)
	b := rankCandidates([]candidate{{Sub: Sub{ID: "b", Language: "tr"}}}, q)
	if a[0].Score != b[0].Score {
		t.Fatalf("HI-only subtitle penalised: %v vs %v", a[0].Score, b[0].Score)
	}
}

func TestFPSMismatchPenalised(t *testing.T) {
	hint := parseRelease("Movie.2010.1080p.BluRay.x264-GRP 23.976fps")
	same := parseRelease("Movie.2010.1080p.BluRay.x264-GRP")
	same.fps = 23.976
	other := same
	other.fps = 25
	if releaseSimilarity(hint, same) <= releaseSimilarity(hint, other) {
		t.Fatal("fps mismatch must lower similarity")
	}
}
