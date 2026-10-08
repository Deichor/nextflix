package images

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
)

const immutable = "public, max-age=31536000, immutable"

// GET/HEAD /tmdb-img/t/p/:size/*file
func (s *Service) serve(c *gin.Context) {
	k, err := parseKey(c.Param("size"), c.Param("file"))
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}

	// One retry covers a file evicted between ensure and open.
	for attempt := 0; attempt < 2; attempt++ {
		fi, hit, err := s.ensure(c.Request.Context(), k)
		if err != nil {
			s.fail(c, err)
			return
		}
		f, err := os.Open(s.store.path(k))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			s.fail(c, err)
			return
		}
		defer f.Close()

		h := c.Writer.Header()
		h.Set("Content-Type", k.contentType())
		h.Set("Cache-Control", immutable)
		h.Set("ETag", etag(k, fi.Size()))
		h.Set("X-Content-Type-Options", "nosniff")
		if k.contentType() == "image/svg+xml" {
			h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
		}
		if hit {
			h.Set("X-Cache", "HIT")
		} else {
			h.Set("X-Cache", "MISS")
		}
		http.ServeContent(c.Writer, c.Request, "", fi.ModTime(), f)
		return
	}
	s.fail(c, errUpstream)
}

func (s *Service) fail(c *gin.Context, err error) {
	switch {
	case errors.Is(err, errNotFound):
		c.Header("Cache-Control", "public, max-age=600")
		c.Status(http.StatusNotFound)
	case errors.Is(err, errUnreachable), errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		c.Header("Cache-Control", "no-store")
		c.Status(http.StatusGatewayTimeout)
	default:
		c.Header("Cache-Control", "no-store")
		c.Status(http.StatusBadGateway)
	}
}

// TMDB file names never change content, so name + length is a stable validator.
func etag(k key, size int64) string {
	h := fnv.New64a()
	h.Write([]byte(k.String()))
	return fmt.Sprintf(`"%x-%x"`, h.Sum64(), size)
}
