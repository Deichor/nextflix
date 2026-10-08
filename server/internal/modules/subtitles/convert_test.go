package subtitles

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/text/encoding/charmap"
)

const turkishVTT = `WEBVTT

00:00:01.000 --> 00:00:03.500
Şimdi Iğdır'a gidiyoruz.
Çok güzel, öyle değil mi?

00:00:04.000 --> 00:00:06.000
<i>İstanbul'da yağmur yağıyor.</i>

00:00:06.500 --> 00:01:09.250
ÇİĞDEM ŞÜKRÜ'YE ÖĞRETMEN DEDİ: "Işık!"

`

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func convert(t *testing.T, raw []byte, name string) string {
	t.Helper()
	out, err := ToWebVTT(raw, name)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return string(out)
}

func TestTurkishEncodings(t *testing.T) {
	cases := map[string]string{
		"tr-cp1254.srt":    "windows-1254",
		"tr-iso8859-9.srt": "windows-1254", // same bytes for these letters; 1254 wins the tie
		"tr-utf8-bom.srt":  "utf-8-bom",
		"tr-utf16le.srt":   "utf-16le",
	}
	for name, charset := range cases {
		raw := readFixture(t, name)
		if _, got := decodeText(raw); got != charset {
			t.Errorf("%s: detected %s, want %s", name, got, charset)
		}
		if got := convert(t, raw, name); got != turkishVTT {
			t.Errorf("%s:\n%s\nwant:\n%s", name, got, turkishVTT)
		}
	}
}

func TestEncodingVariants(t *testing.T) {
	utf8Src := readFixture(t, "tr-utf8-bom.srt")[3:]

	// Plain UTF-8, UTF-16 BE with BOM, BOM-less UTF-16 LE
	be := []byte{0xFE, 0xFF}
	le := []byte{}
	for _, r := range []rune(string(utf8Src)) {
		be = append(be, byte(r>>8), byte(r))
		le = append(le, byte(r), byte(r>>8))
	}
	for name, raw := range map[string][]byte{"utf8": utf8Src, "utf16be": be, "utf16le-nobom": le} {
		if got := convert(t, raw, name+".srt"); got != turkishVTT {
			t.Errorf("%s:\n%s", name, got)
		}
	}

	// UTF-8 that was once decoded as Windows-1252 and saved again ("Ã‡ok gÃ¼zel")
	mojibake, err := charmap.Windows1252.NewDecoder().Bytes(utf8Src)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mojibake), "Ã‡ok") {
		t.Fatalf("fixture is not mojibake: %s", mojibake)
	}
	if got := convert(t, mojibake, "m.srt"); got != turkishVTT {
		t.Errorf("mojibake:\n%s", got)
	}

	// Windows-1252 text without Turkish letters stays as it is.
	fr, _ := charmap.Windows1252.NewEncoder().String("1\n00:00:01,000 --> 00:00:02,000\nCafé crème, “déjà vu”\n")
	if got := convert(t, []byte(fr), "fr.srt"); !strings.Contains(got, "Café crème, “déjà vu”") {
		t.Errorf("windows-1252: %s", got)
	}
}

func TestBrokenSRT(t *testing.T) {
	want := `WEBVTT

00:00:01.000 --> 00:00:02.500
Merhaba dünya!

00:00:02.000 --> 00:00:04.000
<i>Üst satır</i>

00:00:05.500 --> 00:00:07.250
<b>Kalın</b> &amp; <u>altı çizili</u> <i>yarım italik</i>

00:00:08.000 --> 00:00:09.000
Önce gelen
1984

00:00:10.000 --> 00:00:12.000
Sonra gelen

00:00:13.000 --> 00:00:16.000
Sıfır süreli

`
	if got := convert(t, readFixture(t, "broken.srt"), "broken.srt"); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestASS(t *testing.T) {
	want := `WEBVTT

00:00:01.000 --> 00:00:03.200
Bu <i>çok</i> önemli, değil mi?

00:00:05.000 --> 00:00:07.500
Günaydın, nasılsın?
Bugün hava çok güzel.

00:00:08.000 --> 00:00:10.000
<i>Keşke gitmeseydim...</i>

00:00:11.000 --> 00:00:13.000
<b>DİKKAT:</b> Işıklar sönüyor!

`
	if got := convert(t, readFixture(t, "styled.ass"), "styled.ass"); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestVTTNormalised(t *testing.T) {
	in := "\ufeffWEBVTT - Türkçe\r\nKind: captions\r\n\r\nSTYLE\r\n::cue { color: red }\r\n\r\nNOTE bir not\r\n\r\nintro\r\n00:01.000 --> 00:02.000 line:90%\r\n<v Ayşe>Selam <c.yellow>dostum</c></v>\r\n\r\n1\r\n00:00:03.000 --> 00:00:04.000\r\nİkinci\r\n"
	want := "WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nSelam dostum\n\n00:00:03.000 --> 00:00:04.000\nİkinci\n\n"
	if got := convert(t, []byte(in), "x.vtt"); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestMicroDVD(t *testing.T) {
	in := "{1}{1}25\n{25}{75}Merhaba|{y:i}dünya\n{100}{150}{Y:i}Tamamen italik|iki satır\n"
	want := "WEBVTT\n\n00:00:01.000 --> 00:00:03.000\nMerhaba\n<i>dünya</i>\n\n00:00:04.000 --> 00:00:06.000\n<i>Tamamen italik\niki satır</i>\n\n"
	if got := convert(t, []byte(in), "x.sub"); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
	// Without a frame rate line the default 23.976 is used.
	if got := convert(t, []byte("{2398}{4795}Bir\n"), "x.sub"); !strings.Contains(got, "00:01:40.016 --> 00:03:19.991") {
		t.Errorf("default fps: %q", got)
	}
}

func TestUnsupported(t *testing.T) {
	if _, err := ToWebVTT([]byte("just some text\nwithout cues"), "x.srt"); err != ErrUnsupportedFormat {
		t.Fatalf("err = %v", err)
	}
}
