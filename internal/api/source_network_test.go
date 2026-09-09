package api

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"qingzhou/internal/store"
)

type sourceRoundTrip func(*http.Request) (*http.Response, error)

func (f sourceRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type brokenSourceBody struct{ io.Reader }

func (b brokenSourceBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	if err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	return n, err
}
func (brokenSourceBody) Close() error { return nil }

const sourceLink = "trojan://password@node.example:443#new"

func TestFetchSourceFailurePreservesSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		body     string
		readErr  bool
		fetchErr bool
		url      string
	}{
		{name: "HTTP error", status: 502, body: sourceLink},
		{name: "partial content", status: 206, body: sourceLink},
		{name: "no content", status: 204},
		{name: "HTML error page", status: 200, body: "<!doctype html>\n" + sourceLink},
		{name: "invalid subscription", status: 200, body: "not a subscription"},
		{name: "empty subscription", status: 200},
		{name: "truncated after valid node", status: 200, body: sourceLink, readErr: true},
		{name: "oversized after valid node", status: 200, body: sourceLink + "\n" + strings.Repeat(" ", maxSourceBytes)},
		{name: "connection failure", fetchErr: true},
		{name: "invalid URL", url: "file:///etc/passwd"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, st := newResetSubAPI(t)
			gid, err := st.CreateGroup(store.NodeGroup{Name: "last group"})
			if err != nil {
				t.Fatal(err)
			}
			id, err := st.CreateSource(store.NodeSource{Name: "test", URL: "https://source.example/sub", Enabled: true})
			if err != nil {
				t.Fatal(err)
			}
			if err := st.ReplaceSourceNodes(id, []store.Node{{Name: "last good", Protocol: "trojan", ShareLink: sourceLink}}, []int64{gid}, ""); err != nil {
				t.Fatal(err)
			}
			before, _ := st.GetSource(id)
			nodesBefore, _ := st.ListNodes()
			a.sourceClient = &http.Client{Transport: sourceRoundTrip(func(r *http.Request) (*http.Response, error) {
				if tc.fetchErr {
					return nil, errors.New("network unavailable")
				}
				var body io.ReadCloser = io.NopCloser(strings.NewReader(tc.body))
				if tc.readErr {
					body = brokenSourceBody{strings.NewReader(tc.body)}
				}
				return &http.Response{StatusCode: tc.status, Body: body, Header: make(http.Header)}, nil
			})}
			src := *before
			if tc.url != "" {
				src.URL = tc.url
			}
			if n, msg := a.fetchSource(context.Background(), &src, []int64{}); n != 0 || msg == "" {
				t.Fatalf("fetch=%d, %q", n, msg)
			}
			after, _ := st.GetSource(id)
			if after.LastError == "" {
				t.Fatal("missing last_error")
			}
			after.LastError = before.LastError
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("source changed beyond last_error: before=%+v after=%+v", before, after)
			}
			nodesAfter, _ := st.ListNodes()
			if !reflect.DeepEqual(nodesBefore, nodesAfter) {
				t.Fatal("failed fetch changed nodes or group memberships")
			}
		})
	}
}

func TestFetchSourceLimitBoundaryAndRecovery(t *testing.T) {
	a, st := newResetSubAPI(t)
	id, err := st.CreateSource(store.NodeSource{URL: "https://source.example/sub"})
	if err != nil {
		t.Fatal(err)
	}
	src, _ := st.GetSource(id)
	_ = st.ReplaceSourceNodes(id, nil, nil, "previous failure")
	body := sourceLink + "\n" + strings.Repeat(" ", maxSourceBytes-len(sourceLink)-1)
	a.sourceClient = &http.Client{Transport: sourceRoundTrip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{"Content-Type": []string{"text/html"}}}, nil
	})}
	if n, msg := a.fetchSource(context.Background(), src, nil); n != 1 || msg != "" {
		t.Fatalf("fetch=%d %q", n, msg)
	}
	after, _ := st.GetSource(id)
	if after.LastCount != 1 || after.LastError != "" || after.LastFetched == 0 {
		t.Fatalf("source=%+v", after)
	}
	// A database failure partway through replacement must roll back deletion.
	nodesBefore, _ := st.ListNodes()
	if _, err := st.DB().Exec(`CREATE TRIGGER reject_source_insert BEFORE INSERT ON nodes BEGIN SELECT RAISE(FAIL, 'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, msg := a.fetchSource(context.Background(), src, nil); msg == "" {
		t.Fatal("expected database failure")
	}
	nodesAfter, _ := st.ListNodes()
	if !reflect.DeepEqual(nodesBefore, nodesAfter) {
		t.Fatal("failed replacement did not roll back")
	}
}

func TestSourceSyncCancellationDrains(t *testing.T) {
	a, st := newResetSubAPI(t)
	for i := 0; i < 20; i++ {
		if _, err := st.CreateSource(store.NodeSource{URL: "https://source.example/sub", Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	started := make(chan struct{}, 20)
	a.sourceClient = &http.Client{Transport: sourceRoundTrip(func(r *http.Request) (*http.Response, error) {
		started <- struct{}{}
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	a.StartSourceSync(ctx, time.Millisecond, &wg)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("sync did not fetch")
	}
	cancel()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("sync did not drain")
	}
	if len(started) != 0 {
		t.Fatal("sync fetched more sources after cancellation")
	}
}

func TestFetchSourceReusesHTTPConnection(t *testing.T) {
	a, st := newResetSubAPI(t)
	var mu sync.Mutex
	peers := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		peers[r.RemoteAddr] = true
		mu.Unlock()
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, sourceLink)
	}))
	defer srv.Close()
	// The integration fixture is loopback; SSRF behavior is tested independently.
	a.sourceClient = safeFetchClient()
	a.sourceClient.Transport.(*http.Transport).DialContext = (&net.Dialer{}).DialContext
	defer a.Close()
	id, err := st.CreateSource(store.NodeSource{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	src, _ := st.GetSource(id)
	for i := 0; i < 3; i++ {
		if _, msg := a.fetchSource(context.Background(), src, nil); msg != "" {
			t.Fatal(msg)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(peers) != 1 {
		t.Fatalf("used %d connections for 3 fetches", len(peers))
	}
}

func TestFetchSourceUsesLiveGroupsAndDoesNotResurrectDeletedSource(t *testing.T) {
	a, st := newResetSubAPI(t)
	oldGroup, err := st.CreateGroup(store.NodeGroup{Name: "old"})
	if err != nil {
		t.Fatal(err)
	}
	newGroup, err := st.CreateGroup(store.NodeGroup{Name: "new"})
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.CreateSource(store.NodeSource{URL: "https://source.example/sub", GroupIDs: []int64{oldGroup}})
	if err != nil {
		t.Fatal(err)
	}
	src, _ := st.GetSource(id)
	a.sourceClient = &http.Client{Transport: sourceRoundTrip(func(*http.Request) (*http.Response, error) {
		// Simulate an admin edit while a periodic fetch is on the network.
		edited := *src
		edited.GroupIDs = []int64{newGroup}
		if err := st.UpdateSource(edited); err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(sourceLink)), Header: make(http.Header)}, nil
	})}
	if _, msg := a.fetchSource(context.Background(), src, nil); msg != "" {
		t.Fatal(msg)
	}
	nodes, _ := st.ListNodes()
	after, _ := st.GetSource(id)
	if len(nodes) != 1 || !reflect.DeepEqual(nodes[0].GroupIDs, []int64{newGroup}) || !reflect.DeepEqual(after.GroupIDs, []int64{newGroup}) {
		t.Fatal("slow fetch overwrote live group binding")
	}
	if err := st.DeleteSource(id); err != nil {
		t.Fatal(err)
	}
	if _, msg := a.fetchSource(context.Background(), src, nil); msg == "" {
		t.Fatal("fetch resurrected deleted source")
	}
	nodes, _ = st.ListNodes()
	if len(nodes) != 0 {
		t.Fatal("orphaned source nodes created")
	}
}
