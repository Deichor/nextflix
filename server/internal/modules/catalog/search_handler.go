package catalog

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"nextflix/internal/platform/httpserver"
)

// StartSearchIndex loads the typo index from dir and keeps it fresh in the background.
func (s *Service) StartSearchIndex(ctx context.Context, dir string) {
	go NewIndexLoader(s.index, dir).Run(ctx)
}

// SetSearchIndex replaces the typo index (tests).
func (s *Service) SetSearchIndex(ix *TitleIndex) { s.index = ix }

const (
	suggestDefault = 8
	suggestMax     = 15
	// Longer queries are cut: the typo index and ranking cost grows with the query length
	maxQueryRunes = 200
)

// searchQuery reads the q param, trimmed and capped at maxQueryRunes.
func searchQuery(c *gin.Context) string { return clampQuery(c.Query("q")) }

// clampQuery trims s and cuts it to maxQueryRunes runes.
func clampQuery(s string) string {
	s = strings.TrimSpace(s)
	n := 0
	for i := range s {
		if n == maxQueryRunes {
			return strings.TrimSpace(s[:i])
		}
		n++
	}
	return s
}

// SuggestItem is one completion of GET /api/catalog/suggest.
type SuggestItem struct {
	Title     string `json:"title"`
	MediaType string `json:"media_type"`
	ID        int    `json:"id"`
	Original  string `json:"original_title,omitempty"`
}

// GET /api/catalog/suggest?q=&limit= – completions from the typo index only (no TMDB call):
// titles starting with q, titles with a word starting with q, then typo-tolerant prefixes.
func (s *Service) suggest(c *gin.Context) {
	q := searchQuery(c)
	limit := suggestDefault
	if l, err := strconv.Atoi(c.Query("limit")); err == nil && l > 0 {
		limit = min(l, suggestMax)
	}
	items := []SuggestItem{}
	if q != "" {
		for _, h := range s.index.Suggest(q, httpserver.Lang(c), limit) {
			items = append(items, SuggestItem{Title: h.Title, MediaType: h.MediaType, ID: h.ID, Original: h.Original})
		}
	}
	c.JSON(http.StatusOK, gin.H{"query": q, "results": items, "ready": s.index.Ready()})
}
