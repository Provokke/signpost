package shortener

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Server struct {
	store   *Store
	baseURL string
	mux     *http.ServeMux
}

type createRequest struct {
	URL        string `json:"url"`
	Alias      string `json:"alias"`
	TTLSeconds int64  `json:"ttl_seconds"`
}

type linkResponse struct {
	ID       string     `json:"id"`
	ShortURL string     `json:"short_url"`
	URL      string     `json:"url"`
	Created  time.Time  `json:"created"`
	Expires  *time.Time `json:"expires,omitempty"`
	Hits     uint64     `json:"hits"`
}

func NewServer(store *Store, baseURL string) *Server {
	s := &Server{store: store, baseURL: strings.TrimRight(baseURL, "/"), mux: http.NewServeMux()}
	s.mux.HandleFunc("POST /api/links", s.create)
	s.mux.HandleFunc("GET /api/links", s.list)
	s.mux.HandleFunc("GET /api/links/{id}", s.info)
	s.mux.HandleFunc("DELETE /api/links/{id}", s.remove)
	s.mux.HandleFunc("GET /health", s.health)
	s.mux.HandleFunc("GET /{id}", s.redirect)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) respond(link Link) linkResponse {
	out := linkResponse{
		ID:       link.ID,
		ShortURL: s.baseURL + "/" + link.ID,
		URL:      link.URL,
		Created:  link.Created,
		Hits:     link.Hits,
	}
	if !link.Expires.IsZero() {
		expires := link.Expires
		out.Expires = &expires
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func statusFor(err error) int {
	switch {
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrExpired):
		return http.StatusGone
	case errors.Is(err, ErrAliasTaken):
		return http.StatusConflict
	case errors.Is(err, ErrInvalidURL), errors.Is(err, ErrInvalidAlias), errors.Is(err, ErrNegativeTTL):
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}

func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var req createRequest
	if err := decoder.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "request body must be JSON with a url field")
		return
	}
	link, created, err := s.store.Create(req.URL, req.Alias, time.Duration(req.TTLSeconds)*time.Second)
	if err != nil {
		writeError(w, statusFor(err), err.Error())
		return
	}
	status := http.StatusCreated
	if !created {
		status = http.StatusOK
	}
	w.Header().Set("Location", s.baseURL+"/"+link.ID)
	writeJSON(w, status, s.respond(link))
}

func (s *Server) info(w http.ResponseWriter, r *http.Request) {
	link, err := s.store.Get(r.PathValue("id"))
	if err != nil && !errors.Is(err, ErrExpired) {
		writeError(w, statusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.respond(link))
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || limit <= 0 || limit > 100 {
		limit = 20
	}
	links := s.store.List(offset, limit)
	out := make([]linkResponse, 0, len(links))
	for _, link := range links {
		out = append(out, s.respond(link))
	}
	writeJSON(w, http.StatusOK, map[string]any{"links": out, "total": s.store.Len()})
}

func (s *Server) remove(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Delete(r.PathValue("id")); err != nil {
		writeError(w, statusFor(err), err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "links": s.store.Len()})
}

func (s *Server) redirect(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var link Link
	var err error
	if r.Method == http.MethodHead {
		link, err = s.store.Get(id)
	} else {
		link, err = s.store.Visit(id)
	}
	if err != nil {
		writeError(w, statusFor(err), err.Error())
		return
	}
	http.Redirect(w, r, link.URL, http.StatusFound)
}
