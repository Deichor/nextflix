package catalog

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"nextflix/internal/platform/upstream"
)

// Typo index: the most popular titles of TMDB's daily ID exports (original titles plus the
// localized titles searches have returned), searched with trigrams, a phonetic key and edit
// distances. It backs "did you mean" and suggest-as-you-type.

const (
	exportBase       = "https://files.tmdb.org/p/exports/"
	indexMaxTitles   = 200_000
	indexMinPop      = 1.0
	indexMaxAge      = 24 * time.Hour
	indexFile        = "search-index.tsv.gz"
	aliasFile        = "search-aliases.tsv.gz"
	aliasMax         = 50_000
	maxCandidates    = 300
	maxPostingsScan  = 60_000 // a trigram in more titles than this tells nothing
	suggestPopWeight = 0.15
)

// Kinds of index entries.
const (
	kindMovie uint8 = 0
	kindTV    uint8 = 1
)

func kindOf(mediaType string) uint8 {
	if mediaType == "tv" {
		return kindTV
	}
	return kindMovie
}

func mediaTypeOf(k uint8) string {
	if k == kindTV {
		return "tv"
	}
	return "movie"
}

// IndexTitle is one exported title.
type IndexTitle struct {
	Kind  uint8
	ID    int32
	Pop   float32
	Title string // original title
}

// indexName is one searchable name of a title (its original title or a localized alias).
type indexName struct {
	entry int32
	phon  uint32 // hash of the phonetic key of bare
	grams int16  // trigram count of bare
	lang  string // "" = original title
	text  string // display text of an alias ("" = the original title)
	bare  string // normalized, article stripped
}

// indexData is immutable once built. Lookups use sorted slices instead of maps to keep the
// index small (about 200k titles).
type indexData struct {
	titles []IndexTitle
	names  []indexName
	grams  map[uint64][]int32 // trigram -> names (slices of one arena)
	byPhon []int32            // names ordered by phonetic hash
	sorted []int32            // names ordered by bare text (prefix search)
	vocab  []string           // every word of every name, sorted
	keys   []int64            // kind<<32|id of titles, sorted
	keyIdx []int32            // title index of keys[i]
	built  time.Time
	counts sync.Pool // []uint16 per name, reused by candidates
}

// TitleIndex is safe for concurrent use; a rebuild swaps the data atomically.
type TitleIndex struct {
	data atomic.Pointer[indexData]

	mu      sync.Mutex
	aliases map[aliasKey]string // localized titles learned from TMDB answers
	dirty   bool
	pending bool // aliases changed since the last rebuild of the data
}

type aliasKey struct {
	kind uint8
	id   int32
	lang string
}

// NewTitleIndex builds an index from titles (tests and the loaders).
func NewTitleIndex(titles []IndexTitle) *TitleIndex {
	ix := &TitleIndex{aliases: map[aliasKey]string{}}
	ix.data.Store(buildIndexData(titles, nil))
	return ix
}

func newEmptyIndex() *TitleIndex { return &TitleIndex{aliases: map[aliasKey]string{}} }

// Ready reports whether titles are loaded.
func (ix *TitleIndex) Ready() bool {
	d := ix.data.Load()
	return d != nil && len(d.titles) > 0
}

// Size returns the number of titles and names.
func (ix *TitleIndex) Size() (titles, names int) {
	if d := ix.data.Load(); d != nil {
		return len(d.titles), len(d.names)
	}
	return 0, 0
}

func titleKey(kind uint8, id int32) int64 { return int64(kind)<<32 | int64(id) }

func trigrams(s string, fn func(uint64)) int {
	r := []rune("  " + s + " ")
	n := 0
	for i := 0; i+2 < len(r); i++ {
		fn(uint64(r[i])<<42 | uint64(r[i+1])<<21 | uint64(r[i+2]))
		n++
	}
	return n
}

// phonHash is FNV-1a of the phonetic key of a normalized string.
func phonHash(n string) uint32 {
	h := uint32(2166136261)
	for _, c := range []byte(phonetic(n)) {
		h ^= uint32(c)
		h *= 16777619
	}
	return h
}

func buildIndexData(titles []IndexTitle, aliases map[aliasKey]string) *indexData {
	d := &indexData{titles: titles, grams: make(map[uint64][]int32, 1<<16), built: time.Now()}
	words := map[string]struct{}{}
	add := func(entry int32, lang, text string) {
		n := normalize(text)
		if !hasLatin(n) {
			return
		}
		bare := stripArticle(n)
		idx := int32(len(d.names))
		name := indexName{entry: entry, lang: lang, bare: bare, phon: phonHash(bare)}
		if lang != "" {
			name.text = text
		}
		name.grams = int16(min(trigrams(bare, func(g uint64) { d.grams[g] = append(d.grams[g], idx) }), math.MaxInt16))
		d.names = append(d.names, name)
		for _, w := range strings.Fields(n) {
			words[w] = struct{}{}
		}
	}

	d.keys = make([]int64, len(titles))
	order := make([]int32, len(titles))
	for i, t := range titles {
		order[i] = int32(i)
		add(int32(i), "", t.Title)
	}
	sort.Slice(order, func(a, b int) bool {
		return titleKey(titles[order[a]].Kind, titles[order[a]].ID) < titleKey(titles[order[b]].Kind, titles[order[b]].ID)
	})
	for i, ti := range order {
		d.keys[i] = titleKey(titles[ti].Kind, titles[ti].ID)
	}
	d.keyIdx = order
	for k, text := range aliases {
		i, ok := d.title(k.kind, k.id)
		if !ok || strings.EqualFold(text, titles[i].Title) {
			continue
		}
		add(i, k.lang, text)
	}

	// One arena for every posting list (append growth would waste about half)
	total := 0
	for _, p := range d.grams {
		if len(p) <= maxPostingsScan {
			total += len(p)
		}
	}
	arena := make([]int32, 0, total)
	for g, p := range d.grams {
		if len(p) > maxPostingsScan {
			delete(d.grams, g) // never scanned
			continue
		}
		start := len(arena)
		arena = append(arena, p...)
		d.grams[g] = arena[start:len(arena):len(arena)]
	}

	d.vocab = make([]string, 0, len(words))
	for w := range words {
		d.vocab = append(d.vocab, w)
	}
	sort.Strings(d.vocab)
	d.sorted = make([]int32, len(d.names))
	d.byPhon = make([]int32, len(d.names))
	for i := range d.sorted {
		d.sorted[i], d.byPhon[i] = int32(i), int32(i)
	}
	sort.Slice(d.sorted, func(a, b int) bool { return d.names[d.sorted[a]].bare < d.names[d.sorted[b]].bare })
	sort.Slice(d.byPhon, func(a, b int) bool { return d.names[d.byPhon[a]].phon < d.names[d.byPhon[b]].phon })
	return d
}

// title returns the index of a title by kind and TMDB id.
func (d *indexData) title(kind uint8, id int32) (int32, bool) {
	k := titleKey(kind, id)
	i := sort.Search(len(d.keys), func(i int) bool { return d.keys[i] >= k })
	if i < len(d.keys) && d.keys[i] == k {
		return d.keyIdx[i], true
	}
	return 0, false
}

func (d *indexData) knownWord(w string) bool {
	i := sort.SearchStrings(d.vocab, w)
	return i < len(d.vocab) && d.vocab[i] == w
}

// samePhonetic returns the names whose phonetic hash is h.
func (d *indexData) samePhonetic(h uint32) []int32 {
	lo := sort.Search(len(d.byPhon), func(i int) bool { return d.names[d.byPhon[i]].phon >= h })
	hi := lo
	for hi < len(d.byPhon) && d.names[d.byPhon[hi]].phon == h {
		hi++
	}
	return d.byPhon[lo:hi]
}

// Learn remembers a localized title of an indexed title (from a TMDB search answer). The
// names become searchable with the next rebuild (at most every 10 minutes).
func (ix *TitleIndex) Learn(mediaType string, id int, lang, title string) {
	if title == "" || id <= 0 {
		return
	}
	k := aliasKey{kindOf(mediaType), int32(id), lang}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if ix.aliases[k] == title || len(ix.aliases) >= aliasMax {
		return
	}
	if d := ix.data.Load(); d != nil {
		i, ok := d.title(k.kind, k.id)
		if !ok || strings.EqualFold(d.titles[i].Title, title) {
			return
		}
	}
	ix.aliases[k] = title
	ix.dirty, ix.pending = true, true
}

// Hit is a title the index found for a query.
type Hit struct {
	MediaType string  `json:"media_type"`
	ID        int     `json:"id"`
	Title     string  `json:"title"` // the matching name
	Original  string  `json:"original_title,omitempty"`
	Lang      string  `json:"-"` // language of Title ("" = original)
	Score     float64 `json:"-"`
	Sim       float64 `json:"-"`
	Pop       float64 `json:"-"`
}

// popBoost is the popularity weight of index hits.
func popBoost(pop float64) float64 { return 0.06 * math.Log10(1+pop) }

func (d *indexData) hit(ni int32, sim float64) Hit {
	n := d.names[ni]
	t := d.titles[n.entry]
	text := n.text
	if text == "" {
		text = t.Title
	}
	h := Hit{MediaType: mediaTypeOf(t.Kind), ID: int(t.ID), Title: text, Lang: n.lang, Sim: sim, Pop: float64(t.Pop)}
	if n.lang != "" {
		h.Original = t.Title
	}
	h.Score = sim + popBoost(h.Pop)
	return h
}

// candidates returns names sharing trigrams with bare (most shared first) plus names with
// the same phonetic key.
func (d *indexData) candidates(bare string, limit int) []int32 {
	counts, _ := d.counts.Get().([]uint16)
	if len(counts) != len(d.names) {
		counts = make([]uint16, len(d.names))
	}
	var touched []int32
	qn := trigrams(bare, func(g uint64) {
		if p := d.grams[g]; len(p) <= maxPostingsScan {
			for _, ni := range p {
				if counts[ni] == 0 {
					touched = append(touched, ni)
				}
				counts[ni]++
			}
		}
	})
	need := max(1, qn/4)
	type cand struct {
		ni   int32
		dice float64
	}
	cs := make([]cand, 0, 256)
	for _, ni := range touched {
		c := int(counts[ni])
		counts[ni] = 0
		if c < need {
			continue
		}
		cs = append(cs, cand{ni, 2 * float64(c) / float64(qn+int(d.names[ni].grams))})
	}
	d.counts.Put(counts)
	sort.Slice(cs, func(a, b int) bool { return cs[a].dice > cs[b].dice })
	if len(cs) > limit {
		cs = cs[:limit]
	}
	out := make([]int32, 0, len(cs)+8)
	seen := make(map[int32]bool, len(cs))
	for _, c := range cs {
		out = append(out, c.ni)
		seen[c.ni] = true
	}
	for _, ni := range d.samePhonetic(phonHash(bare)) {
		if !seen[ni] {
			out = append(out, ni)
		}
	}
	return out
}

// nameSim compares the query with a whole name: edit distance, Jaro-Winkler, trigram overlap
// and the phonetic key.
func nameSim(qb string, qr []rune, qphon uint32, n *indexName) float64 {
	if qb == n.bare {
		return 1
	}
	nr := []rune(n.bare)
	dl := 1 - float64(damerauLevenshtein(qr, nr))/float64(max(len(qr), len(nr)))
	sim := 0.6*dl + 0.4*jaroWinkler(qr, nr)
	if qphon == n.phon {
		sim += 0.08
	}
	return min(sim, 0.99)
}

// Correct returns the titles closest to q (typo tolerant), best first; only hits with a
// similarity of at least minSim.
func (ix *TitleIndex) Correct(q string, limit int, minSim float64) []Hit {
	d := ix.data.Load()
	if d == nil {
		return nil
	}
	qb := stripArticle(normalize(q))
	if len([]rune(qb)) < 3 || !hasLatin(qb) {
		return nil
	}
	qr, qphon := []rune(qb), phonHash(qb)
	var hits []Hit
	for _, ni := range d.candidates(qb, maxCandidates) {
		n := &d.names[ni]
		// Lengths far apart cannot be a typo of each other
		if l := len([]rune(n.bare)); l > 2*len(qr)+2 || 2*l+2 < len(qr) {
			continue
		}
		if sim := nameSim(qb, qr, qphon, n); sim >= minSim {
			hits = append(hits, d.hit(ni, sim))
		}
	}
	return topHits(hits, limit)
}

// topHits orders hits by score and keeps the best hit per title.
func topHits(hits []Hit, limit int) []Hit {
	sort.SliceStable(hits, func(a, b int) bool { return hits[a].Score > hits[b].Score })
	seen := map[string]bool{}
	out := hits[:0]
	for _, h := range hits {
		k := h.MediaType + ":" + strconv.Itoa(h.ID)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, h)
		if len(out) == limit {
			break
		}
	}
	return out
}

// KnownWords reports whether every word of q (3+ letters, articles aside) appears in some
// indexed title. A query with an unknown word probably has a typo.
func (ix *TitleIndex) KnownWords(q string) bool {
	d := ix.data.Load()
	if d == nil {
		return true
	}
	for _, w := range strings.Fields(normalize(q)) {
		if len(w) < 3 || !hasLatin(w) {
			continue
		}
		if !d.knownWord(w) {
			return false
		}
	}
	return true
}

// Suggest completes a partly typed query: titles starting with it, titles with a word
// starting with it, then typo-tolerant prefix matches. Names in lang are preferred; one
// suggestion per title and per display text.
func (ix *TitleIndex) Suggest(q, lang string, limit int) []Hit {
	d := ix.data.Load()
	if d == nil || limit <= 0 {
		return nil
	}
	qn := normalize(q)
	qb := stripArticle(qn)
	if qb == "" || !hasLatin(qb) {
		return nil
	}
	var hits []Hit
	inLang := func(n *indexName) bool { return n.lang == "" || n.lang == lang }
	// 1. names starting with the query (article ignored)
	lo := sort.Search(len(d.sorted), func(i int) bool { return d.names[d.sorted[i]].bare >= qb })
	for i := lo; i < len(d.sorted) && i-lo < 20_000; i++ {
		ni := d.sorted[i]
		n := &d.names[ni]
		if !strings.HasPrefix(n.bare, qb) {
			break
		}
		if inLang(n) {
			sim := 1.0
			if n.lang == lang && lang != "" {
				sim += 0.05
			}
			hits = append(hits, d.hit(ni, sim))
		}
	}
	// 2. a later word starts with the query, 3. fuzzy prefix
	if len(qb) >= 3 {
		qr, qphon := []rune(qb), phonHash(qb)
		for _, ni := range d.candidates(qb, maxCandidates) {
			n := &d.names[ni]
			if !inLang(n) || strings.HasPrefix(n.bare, qb) {
				continue
			}
			if strings.Contains(" "+n.bare, " "+qb) {
				hits = append(hits, d.hit(ni, 0.85))
				continue
			}
			if len(qr) < 4 {
				continue
			}
			// Compare with the start of the name, one letter longer than the query
			nr := []rune(n.bare)
			cut := min(len(nr), len(qr)+1)
			prefix := indexName{bare: string(nr[:cut]), phon: phonHash(string(nr[:cut]))}
			if sim := nameSim(qb, qr, qphon, &prefix); sim >= 0.8 {
				hits = append(hits, d.hit(ni, 0.6*sim+0.1))
			}
		}
	}
	// Completions lean on popularity more than corrections: "thron" -> Game of Thrones first
	for i := range hits {
		hits[i].Score = hits[i].Sim + suggestPopWeight*math.Log10(1+hits[i].Pop)
	}
	sort.SliceStable(hits, func(a, b int) bool { return hits[a].Score > hits[b].Score })
	seenTitle, seenText := map[string]bool{}, map[string]bool{}
	out := make([]Hit, 0, limit)
	for _, h := range hits {
		k, text := h.MediaType+":"+strconv.Itoa(h.ID), normalize(h.Title)
		if seenTitle[k] || seenText[text] {
			continue
		}
		seenTitle[k], seenText[text] = true, true
		out = append(out, h)
		if len(out) == limit {
			break
		}
	}
	return out
}

// ---- Loading, building from the TMDB exports, persistence ----

type exportLine struct {
	Adult         bool    `json:"adult"`
	ID            int32   `json:"id"`
	OriginalTitle string  `json:"original_title"`
	OriginalName  string  `json:"original_name"`
	Popularity    float32 `json:"popularity"`
	Video         bool    `json:"video"`
}

// parseExport reads a gzipped TMDB ID export and keeps titles of at least minPop.
func parseExport(r io.Reader, kind uint8, minPop float32) ([]IndexTitle, error) {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	var out []IndexTitle
	sc := bufio.NewScanner(zr)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		// Cheap pre-filter: most lines are below the popularity cut
		if i := bytes.Index(line, []byte(`"popularity":`)); i >= 0 {
			rest := line[i+13:]
			end := bytes.IndexAny(rest, ",}")
			if end > 0 {
				if p, err := strconv.ParseFloat(string(rest[:end]), 32); err == nil && float32(p) < minPop {
					continue
				}
			}
		}
		var l exportLine
		if json.Unmarshal(line, &l) != nil || l.Adult || l.Video || l.Popularity < minPop {
			continue
		}
		title := l.OriginalTitle
		if kind == kindTV {
			title = l.OriginalName
		}
		title = strings.TrimSpace(title)
		if title == "" || !hasLatin(normalize(title)) {
			continue
		}
		out = append(out, IndexTitle{Kind: kind, ID: l.ID, Pop: l.Popularity, Title: title})
	}
	return out, sc.Err()
}

// topTitles keeps the n most popular titles.
func topTitles(titles []IndexTitle, n int) []IndexTitle {
	sort.Slice(titles, func(a, b int) bool { return titles[a].Pop > titles[b].Pop })
	if len(titles) > n {
		titles = titles[:n]
	}
	return titles
}

// IndexLoader keeps a TitleIndex fresh: it loads the saved index at start, rebuilds it from
// the TMDB exports once a day and saves the learned aliases.
type IndexLoader struct {
	Index *TitleIndex
	Dir   string
	http  *upstream.Client
	base  string
}

func NewIndexLoader(ix *TitleIndex, dir string) *IndexLoader {
	return &IndexLoader{
		Index: ix,
		Dir:   dir,
		base:  exportBase,
		// The exports are big (movies ~28 MB): no hedging, a long budget, stalls still retried
		http: upstream.New(upstream.Options{
			Attempts:        3,
			Budget:          4 * time.Minute,
			MaxBody:         128 << 20,
			HeaderTimeout:   15 * time.Second,
			ReadIdleTimeout: 15 * time.Second,
			UserAgent:       "Nextflix",
			RetryStatus:     func(code int) bool { return code == http.StatusTooManyRequests || code >= 500 },
		}),
	}
}

// Run loads the saved index, then refreshes it when it is older than a day (checked every
// hour), and rebuilds the searchable names when aliases were learned (every 10 minutes).
func (l *IndexLoader) Run(ctx context.Context) {
	built, err := l.load()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("catalog: search index: load: %v", err)
	}
	refresh := time.NewTicker(time.Hour)
	aliases := time.NewTicker(10 * time.Minute)
	defer refresh.Stop()
	defer aliases.Stop()
	for {
		if time.Since(built) >= indexMaxAge {
			if t, err := l.Refresh(ctx); err != nil {
				log.Printf("catalog: search index: refresh: %v", err)
			} else {
				built = t
			}
		}
		select {
		case <-ctx.Done():
			l.saveAliases()
			return
		case <-aliases.C:
			l.Index.rebuildAliases()
			l.saveAliases()
		case <-refresh.C:
		}
	}
}

// Refresh downloads the newest movie and TV exports (today's, else up to 3 days back),
// builds the index and saves it.
func (l *IndexLoader) Refresh(ctx context.Context) (time.Time, error) {
	start := time.Now()
	var all []IndexTitle
	for _, kind := range []uint8{kindMovie, kindTV} {
		titles, err := l.download(ctx, kind)
		if err != nil {
			return time.Time{}, err
		}
		all = append(all, titles...)
	}
	all = topTitles(all, indexMaxTitles)
	l.Index.replace(all)
	if err := l.save(all); err != nil {
		log.Printf("catalog: search index: save: %v", err)
	}
	t, n := l.Index.Size()
	log.Printf("catalog: search index: %d titles, %d names built in %s", t, n, time.Since(start).Round(time.Millisecond))
	return time.Now(), nil
}

func (l *IndexLoader) download(ctx context.Context, kind uint8) ([]IndexTitle, error) {
	name := "movie_ids"
	if kind == kindTV {
		name = "tv_series_ids"
	}
	var lastErr error
	for back := 0; back <= 3; back++ {
		day := time.Now().UTC().AddDate(0, 0, -back)
		u := l.base + name + "_" + day.Format("01_02_2006") + ".json.gz"
		resp, err := l.http.Get(ctx, u, nil)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.Status != http.StatusOK {
			// Today's file appears around 8:00 UTC; before that S3 answers 403
			lastErr = fmt.Errorf("%s: status %d", u, resp.Status)
			continue
		}
		return parseExport(bytes.NewReader(resp.Body), kind, indexMinPop)
	}
	return nil, lastErr
}

// replace swaps in new titles (the learned aliases are kept).
func (ix *TitleIndex) replace(titles []IndexTitle) {
	ix.mu.Lock()
	aliases := make(map[aliasKey]string, len(ix.aliases))
	for k, v := range ix.aliases {
		aliases[k] = v
	}
	ix.pending = false
	ix.mu.Unlock()
	ix.data.Store(buildIndexData(titles, aliases))
}

// rebuildAliases makes the aliases learned since the last build searchable.
func (ix *TitleIndex) rebuildAliases() {
	ix.mu.Lock()
	pending := ix.pending
	ix.mu.Unlock()
	if d := ix.data.Load(); pending && d != nil {
		ix.replace(d.titles)
	}
}

func (l *IndexLoader) path(name string) string { return filepath.Join(l.Dir, name) }

// writeGz writes lines to a gzip file atomically (temp file + rename).
func writeGz(path string, write func(w *bufio.Writer) error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	zw := gzip.NewWriter(f)
	bw := bufio.NewWriter(zw)
	err = write(bw)
	if err == nil {
		err = bw.Flush()
	}
	if err == nil {
		err = zw.Close()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func cleanField(s string) string {
	return strings.NewReplacer("\t", " ", "\n", " ", "\r", " ").Replace(s)
}

// save writes the titles as "kind\tid\tpopularity\ttitle" lines.
func (l *IndexLoader) save(titles []IndexTitle) error {
	return writeGz(l.path(indexFile), func(w *bufio.Writer) error {
		for _, t := range titles {
			fmt.Fprintf(w, "%d\t%d\t%.4g\t%s\n", t.Kind, t.ID, t.Pop, cleanField(t.Title))
		}
		return nil
	})
}

func (l *IndexLoader) saveAliases() {
	ix := l.Index
	ix.mu.Lock()
	if !ix.dirty {
		ix.mu.Unlock()
		return
	}
	lines := make([]string, 0, len(ix.aliases))
	for k, v := range ix.aliases {
		lines = append(lines, fmt.Sprintf("%d\t%d\t%s\t%s\n", k.kind, k.id, k.lang, cleanField(v)))
	}
	ix.dirty = false
	ix.mu.Unlock()
	if err := writeGz(l.path(aliasFile), func(w *bufio.Writer) error {
		for _, s := range lines {
			w.WriteString(s)
		}
		return nil
	}); err != nil {
		log.Printf("catalog: search index: save aliases: %v", err)
	}
}

func readGzLines(path string, fn func(fields []string)) (time.Time, error) {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return time.Time{}, err
	}
	zr, err := gzip.NewReader(f)
	if err != nil {
		return time.Time{}, err
	}
	sc := bufio.NewScanner(zr)
	for sc.Scan() {
		fn(strings.SplitN(sc.Text(), "\t", 4))
	}
	return st.ModTime(), sc.Err()
}

// load reads the saved aliases and index; it returns when the index was built.
func (l *IndexLoader) load() (time.Time, error) {
	aliases := map[aliasKey]string{}
	readGzLines(l.path(aliasFile), func(f []string) {
		if len(f) == 4 {
			k, _ := strconv.Atoi(f[0])
			id, _ := strconv.Atoi(f[1])
			aliases[aliasKey{uint8(k), int32(id), f[2]}] = f[3]
		}
	})
	l.Index.mu.Lock()
	for k, v := range aliases {
		l.Index.aliases[k] = v
	}
	l.Index.mu.Unlock()

	start := time.Now()
	var titles []IndexTitle
	mod, err := readGzLines(l.path(indexFile), func(f []string) {
		if len(f) != 4 {
			return
		}
		k, _ := strconv.Atoi(f[0])
		id, _ := strconv.Atoi(f[1])
		pop, _ := strconv.ParseFloat(f[2], 32)
		titles = append(titles, IndexTitle{Kind: uint8(k), ID: int32(id), Pop: float32(pop), Title: f[3]})
	})
	if err != nil {
		return time.Time{}, err
	}
	l.Index.replace(titles)
	t, n := l.Index.Size()
	log.Printf("catalog: search index: loaded %d titles, %d names in %s (built %s)", t, n,
		time.Since(start).Round(time.Millisecond), mod.Format(time.RFC3339))
	return mod, nil
}
