package tmdb

// WithBaseURL returns a view of the client that talks to base instead of the TMDB API (fake
// servers in tests of other packages). It shares the cache.
func (c *Client) WithBaseURL(base string) *Client {
	cp := *c
	cp.base = base
	return &cp
}
