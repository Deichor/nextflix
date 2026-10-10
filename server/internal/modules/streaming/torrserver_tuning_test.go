package streaming

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TorrServer refuses the settings during every tuning attempt: the DB prune still starts.
func TestDBPruneStartsWhenTuningFails(t *testing.T) {
	old := tsTuneRetryStep
	tsTuneRetryStep = time.Millisecond
	t.Cleanup(func() { tsTuneRetryStep = old })
	listed := make(chan struct{}, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/torrents" {
			select {
			case listed <- struct{}{}:
			default:
			}
			fmt.Fprint(w, "[]")
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(ts.Close)
	s := NewService(nil, Config{TorrServerURL: ts.URL})
	s.tuneTorrServerRetrying()
	select {
	case <-listed:
	case <-time.After(5 * time.Second):
		t.Fatal("no DB prune after the tuning failed")
	}
}
