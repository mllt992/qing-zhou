package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

func TestTimeoutResponseReachesClientBeforeWriteDeadline(t *testing.T) {
	if ServerWriteTimeout-RequestTimeout < 10*time.Second {
		t.Fatal("insufficient response-write margin")
	}
	// Exercise real socket deadlines and the production middleware ordering at
	// scaled durations. chi's timeout is cooperative: handlers return on Done
	// without writing another status, letting the middleware emit its 504.
	r := chi.NewRouter()
	r.Use(middleware.Compress(5))
	r.Use(middleware.Timeout(100 * time.Millisecond))
	r.Get("/", func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	srv := httptest.NewUnstartedServer(r)
	srv.Config.WriteTimeout = 1500 * time.Millisecond
	srv.Start()
	defer srv.Close()
	client := srv.Client()
	client.Timeout = 2 * time.Second
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("lost timeout response: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}
