package shortener

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type harness struct {
	t      *testing.T
	clock  *fakeClock
	server *Server
}

func newHarness(t *testing.T) *harness {
	clock := newClock()
	store, _ := openTemp(t, clock)
	return &harness{t: t, clock: clock, server: NewServer(store, "https://sho.rt/")}
}

func (h *harness) do(method, path, body string) *httptest.ResponseRecorder {
	h.t.Helper()
	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	} else {
		reader = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, reader)
	rec := httptest.NewRecorder()
	h.server.ServeHTTP(rec, req)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder, into any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), into); err != nil {
		t.Fatalf("bad json %q: %v", rec.Body.String(), err)
	}
}

func TestCreateAndRedirect(t *testing.T) {
	h := newHarness(t)
	rec := h.do("POST", "/api/links", `{"url": "https://example.com/long/path"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var link linkResponse
	decode(t, rec, &link)
	if link.ID != "1001" || link.ShortURL != "https://sho.rt/1001" {
		t.Fatalf("link = %+v", link)
	}
	if rec.Header().Get("Location") != "https://sho.rt/1001" {
		t.Fatalf("Location = %q", rec.Header().Get("Location"))
	}
	redirect := h.do("GET", "/1001", "")
	if redirect.Code != http.StatusFound || redirect.Header().Get("Location") != "https://example.com/long/path" {
		t.Fatalf("redirect %d to %q", redirect.Code, redirect.Header().Get("Location"))
	}
}

func TestCreateSameURLReturnsOK(t *testing.T) {
	h := newHarness(t)
	h.do("POST", "/api/links", `{"url": "https://example.com/"}`)
	rec := h.do("POST", "/api/links", `{"url": "https://example.com/"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestCreateWithAliasAndTTL(t *testing.T) {
	h := newHarness(t)
	rec := h.do("POST", "/api/links", `{"url": "https://example.com/", "alias": "sale", "ttl_seconds": 60}`)
	var link linkResponse
	decode(t, rec, &link)
	if link.ID != "sale" || link.Expires == nil || !link.Expires.Equal(h.clock.Now().Add(time.Minute)) {
		t.Fatalf("link = %+v", link)
	}
	h.clock.Advance(time.Minute)
	if code := h.do("GET", "/sale", "").Code; code != http.StatusGone {
		t.Fatalf("expired redirect status %d, want 410", code)
	}
}

func TestCreateValidationErrors(t *testing.T) {
	h := newHarness(t)
	cases := map[string]struct {
		body string
		code int
	}{
		"not json":      {`hello`, 400},
		"unknown field": {`{"url": "https://a.io", "x": 1}`, 400},
		"missing url":   {`{}`, 400},
		"bad scheme":    {`{"url": "ftp://a.io"}`, 400},
		"bad alias":     {`{"url": "https://a.io", "alias": "a b"}`, 400},
		"negative ttl":  {`{"url": "https://a.io", "ttl_seconds": -5}`, 400},
	}
	for name, c := range cases {
		rec := h.do("POST", "/api/links", c.body)
		if rec.Code != c.code {
			t.Errorf("%s: status %d, want %d", name, rec.Code, c.code)
		}
		var body map[string]string
		decode(t, rec, &body)
		if body["error"] == "" {
			t.Errorf("%s: no error message", name)
		}
	}
}

func TestAliasConflict(t *testing.T) {
	h := newHarness(t)
	h.do("POST", "/api/links", `{"url": "https://a.io", "alias": "taken"}`)
	rec := h.do("POST", "/api/links", `{"url": "https://b.io", "alias": "taken"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409", rec.Code)
	}
}

func TestInfoReportsHits(t *testing.T) {
	h := newHarness(t)
	h.do("POST", "/api/links", `{"url": "https://a.io"}`)
	h.do("GET", "/1001", "")
	h.do("GET", "/1001", "")
	rec := h.do("GET", "/api/links/1001", "")
	var link linkResponse
	decode(t, rec, &link)
	if rec.Code != 200 || link.Hits != 2 {
		t.Fatalf("status %d link %+v", rec.Code, link)
	}
}

func TestHeadDoesNotCountAsAHit(t *testing.T) {
	h := newHarness(t)
	h.do("POST", "/api/links", `{"url": "https://a.io"}`)
	if rec := h.do("HEAD", "/1001", ""); rec.Code != http.StatusFound {
		t.Fatalf("HEAD status %d", rec.Code)
	}
	var link linkResponse
	decode(t, h.do("GET", "/api/links/1001", ""), &link)
	if link.Hits != 0 {
		t.Fatalf("hits = %d", link.Hits)
	}
}

func TestUnknownLinkIs404(t *testing.T) {
	h := newHarness(t)
	for _, path := range []string{"/nope", "/api/links/nope"} {
		if code := h.do("GET", path, "").Code; code != http.StatusNotFound {
			t.Errorf("%s status %d", path, code)
		}
	}
}

func TestDeleteEndpoint(t *testing.T) {
	h := newHarness(t)
	h.do("POST", "/api/links", `{"url": "https://a.io"}`)
	if code := h.do("DELETE", "/api/links/1001", "").Code; code != http.StatusNoContent {
		t.Fatalf("delete status %d", code)
	}
	if code := h.do("GET", "/1001", "").Code; code != http.StatusNotFound {
		t.Fatalf("after delete status %d", code)
	}
	if code := h.do("DELETE", "/api/links/1001", "").Code; code != http.StatusNotFound {
		t.Fatalf("second delete status %d", code)
	}
}

func TestListEndpoint(t *testing.T) {
	h := newHarness(t)
	for _, name := range []string{"a", "b", "c"} {
		h.do("POST", "/api/links", `{"url": "https://example.com/`+name+`"}`)
		h.clock.Advance(time.Second)
	}
	rec := h.do("GET", "/api/links?limit=2", "")
	var body struct {
		Links []linkResponse `json:"links"`
		Total int            `json:"total"`
	}
	decode(t, rec, &body)
	if len(body.Links) != 2 || body.Total != 3 || body.Links[0].URL != "https://example.com/c" {
		t.Fatalf("body = %+v", body)
	}
}

func TestHealthEndpoint(t *testing.T) {
	h := newHarness(t)
	rec := h.do("GET", "/health", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"status":"ok"`) {
		t.Fatalf("health %d %s", rec.Code, rec.Body)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	h := newHarness(t)
	if code := h.do("PUT", "/api/links", "").Code; code != http.StatusMethodNotAllowed {
		t.Fatalf("status %d, want 405", code)
	}
}

