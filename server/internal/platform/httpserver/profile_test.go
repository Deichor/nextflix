package httpserver

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestProfileID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		header, query string
		want          uint
	}{
		{"", "", 0},
		{"7", "", 7},
		{"7", "9", 7}, // header wins
		{"", "9", 9},
		{"abc", "9", 9}, // invalid header: query
		{"", "abc", 0},
		{"-1", "", 0},
		{"", "-1", 0},
	}
	for _, tc := range cases {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		path := "/"
		if tc.query != "" {
			path += "?profile_id=" + tc.query
		}
		c.Request = httptest.NewRequest(http.MethodGet, path, nil)
		if tc.header != "" {
			c.Request.Header.Set("X-User-ID", tc.header)
		}
		if got := ProfileID(c); got != tc.want {
			t.Errorf("header %q, query %q: got %d, want %d", tc.header, tc.query, got, tc.want)
		}
	}
}
