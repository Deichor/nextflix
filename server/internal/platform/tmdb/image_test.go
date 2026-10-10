package tmdb

import "testing"

// Clients load images only through /tmdb-img (CLAUDE.md), never from image.tmdb.org.
func TestImageURL(t *testing.T) {
	for _, tc := range []struct{ path, size, want string }{
		{"", "w500", ""},
		{"/abc.jpg", "w500", "/tmdb-img/t/p/w500/abc.jpg"},
		{"abc.jpg", "w1280", "/tmdb-img/t/p/w1280/abc.jpg"},
		{"https://image.tmdb.org/t/p/w342/abc.jpg", "w500", "/tmdb-img/t/p/w342/abc.jpg"},
		{"/tmdb-img/t/p/w342/abc.jpg", "w500", "/tmdb-img/t/p/w342/abc.jpg"},
		{"https://example.com/x.png", "w500", "https://example.com/x.png"},
	} {
		if got := ImageURL(tc.path, tc.size); got != tc.want {
			t.Errorf("ImageURL(%q, %q) = %q, want %q", tc.path, tc.size, got, tc.want)
		}
	}
}
