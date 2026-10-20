package subtitles

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
)

const creditAnswer = `POST "https://api.anthropic.com/v1/messages": 400 Bad Request {"type":"error","error":{"type":"invalid_request_error","message":"Your credit balance is too low to access the Anthropic API. Please go to Plans & Billing to upgrade or purchase credits."},"request_id":"req_011CTestRequestId"}`

func TestClaudeAPIErrorNamesTheCreditBalance(t *testing.T) {
	e, ok := claudeAPIError(400, creditAnswer)
	if !ok || e.Kind != aiErrCredit || strings.Contains(e.Error(), "req_") || strings.Contains(e.Error(), "anthropic.com") {
		t.Fatalf("credit: %v %+v", ok, e)
	}
	e, ok = claudeAPIError(400, `POST "x": 400 Bad Request {"type":"error","error":{"message":"max_tokens: too large \"x\""},"request_id":"req_1"}`)
	if !ok || e.Kind != aiErrRejected || e.Msg != `Claude rejected the request: max_tokens: too large "x"` {
		t.Fatalf("rejected: %+v", e)
	}
	if e, ok = claudeAPIError(401, ""); !ok || e.Kind != aiErrKey {
		t.Fatalf("401: %+v", e)
	}
	if _, ok = claudeAPIError(529, "overloaded"); ok {
		t.Fatal("an overload is not an account error")
	}
}

func TestAIAccountErrorHoldsEveryTranslation(t *testing.T) {
	fastAI(t)
	gin.SetMode(gin.TestMode)
	var calls atomic.Int32
	credit, _ := claudeAPIError(400, creditAnswer)
	s := aiTestService(t, func(context.Context, string, string, string) (string, aiTokens, error) {
		calls.Add(1)
		return "", aiTokens{}, credit
	})
	r := gin.New()
	Register(r.Group("/api"), s)
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		return w
	}
	src := embeddedSource(t, s, renderVTT(numberedCues(10, 4), false))
	en := Sub{ID: src, Source: SourceEmbedded, Language: "en", Synced: true}
	q := Query{MediaType: "movie", TmdbID: 5, Langs: []string{"tr", "en"}}
	listed := func() bool {
		for _, x := range s.addAI([]Sub{en}, q, []string{"tr"}) {
			if x.Source == SourceAI {
				return true
			}
		}
		return false
	}
	if !listed() {
		t.Fatal("no AI item before the error")
	}

	// The file answers 502 ai:true with the kind, and the account error is kept for every translation
	file := "/api/subtitles/file?id=" + url.QueryEscape(aiID("tr", aiMeta{"movie", 5, 0, 0}, src))
	w := get(file)
	var body struct {
		AI   bool   `json:"ai"`
		Kind string `json:"error_kind"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if w.Code != http.StatusBadGateway || !body.AI || body.Kind != aiErrCredit {
		t.Fatalf("file: %d %s", w.Code, w.Body)
	}
	if listed() {
		t.Fatal("AI item listed while the account has no credit")
	}
	w = get("/api/settings/subtitles")
	var v SettingsView
	_ = json.Unmarshal(w.Body.Bytes(), &v)
	if v.AI.ErrorKind != aiErrCredit || v.AI.ErrorAt == "" || !strings.Contains(v.AI.Error, "credit") {
		t.Fatalf("settings: %s", w.Body)
	}

	// Asked again, nothing is sent to Claude while the error holds
	if w = get(file); w.Code != http.StatusBadGateway {
		t.Fatalf("second file: %d %s", w.Code, w.Body)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("Claude asked %d times", n)
	}

	// A new key ends it
	key := "NEWKEY"
	var in SettingsInput
	in.AI = &struct {
		Enabled *bool     `json:"enabled"`
		APIKey  optString `json:"api_key"`
		Langs   *[]string `json:"langs"`
	}{APIKey: optString{Set: true, Value: key}}
	v, err := s.UpdateSettings(in)
	if err != nil || v.AI.Error != "" || !listed() {
		t.Fatalf("after a new key: %v %+v listed %v", err, v.AI, listed())
	}
}

func TestPassingKeyTestEndsTheAccountError(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	s := aiTestService(t, func(context.Context, string, string, string) (string, aiTokens, error) {
		if fail.Load() {
			return "", aiTokens{}, AIKeyError{"the Claude account has no credit left", aiErrCredit}
		}
		return `{"lines":[]}`, aiTokens{}, nil
	})
	// Another key's test says nothing about the stored one
	if err := s.TestAIKey(context.Background(), "OTHER"); err == nil {
		t.Fatal("test passed")
	}
	if e, _ := s.AIAccountError(); e != nil {
		t.Fatal("another key's failure held the stored key")
	}
	if err := s.TestAIKey(context.Background(), ""); err == nil {
		t.Fatal("test passed")
	}
	if e, _ := s.AIAccountError(); e == nil || e.Kind != aiErrCredit {
		t.Fatalf("stored key's failure not kept: %v", e)
	}
	fail.Store(false)
	if err := s.TestAIKey(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if e, _ := s.AIAccountError(); e != nil {
		t.Fatal("a passing test kept the error")
	}
}
