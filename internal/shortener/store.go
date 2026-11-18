package shortener

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"sort"
	"sync"
	"time"
)

var (
	ErrNotFound     = errors.New("link not found")
	ErrExpired      = errors.New("link has expired")
	ErrAliasTaken   = errors.New("alias is already in use")
	ErrInvalidURL   = errors.New("url must be an absolute http or https address")
	ErrInvalidAlias = errors.New("alias must be 3 to 32 letters, digits, dashes or underscores")
	ErrNegativeTTL  = errors.New("ttl must not be negative")
)

const (
	MaxURLLength = 2048
	seqOffset    = 62 * 62 * 62
)

var aliasPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{3,32}$`)

var reserved = map[string]bool{"api": true, "health": true, "favicon.ico": true}

type Link struct {
	ID      string    `json:"id"`
	URL     string    `json:"url"`
	Created time.Time `json:"created"`
	Expires time.Time `json:"expires,omitempty"`
	Hits    uint64    `json:"hits"`
	Custom  bool      `json:"custom"`
}

type record struct {
	Op      string `json:"op"`
	ID      string `json:"id"`
	URL     string `json:"url,omitempty"`
	Created int64  `json:"created,omitempty"`
	Expires int64  `json:"expires,omitempty"`
	Hits    uint64 `json:"hits,omitempty"`
	Seq     uint64 `json:"seq,omitempty"`
	Custom  bool   `json:"custom,omitempty"`
}

type Store struct {
	mu     sync.Mutex
	path   string
	file   *os.File
	clock  func() time.Time
	links  map[string]*Link
	byURL  map[string]string
	seq    uint64
	closed bool
}

func OpenStore(path string, clock func() time.Time) (*Store, error) {
	if clock == nil {
		clock = time.Now
	}
	s := &Store{
		path:  path,
		clock: clock,
		links: make(map[string]*Link),
		byURL: make(map[string]string),
		seq:   seqOffset,
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	s.file = file
	return s, nil
}

func (s *Store) load() error {
	file, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	reader := bufio.NewReader(file)
	var good int64
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			var rec record
			parseErr := json.Unmarshal(bytes.TrimSpace(line), &rec)
			complete := line[len(line)-1] == '\n'
			if parseErr != nil {
				file.Close()
				if !complete {
					return os.Truncate(s.path, good)
				}
				return fmt.Errorf("corrupt store at byte %d: %w", good, parseErr)
			}
			s.apply(rec)
			good += int64(len(line))
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			file.Close()
			return readErr
		}
	}
	return file.Close()
}

func (s *Store) apply(rec record) {
	switch rec.Op {
	case "put":
		link := &Link{ID: rec.ID, URL: rec.URL, Created: time.Unix(rec.Created, 0).UTC(), Hits: rec.Hits, Custom: rec.Custom}
		if rec.Expires != 0 {
			link.Expires = time.Unix(rec.Expires, 0).UTC()
		}
		s.links[rec.ID] = link
		if !link.Custom && link.Expires.IsZero() {
			s.byURL[link.URL] = link.ID
		}
		if rec.Seq > s.seq {
			s.seq = rec.Seq
		}
	case "seq":
		if rec.Seq > s.seq {
			s.seq = rec.Seq
		}
	case "hit":
		if link, ok := s.links[rec.ID]; ok {
			link.Hits++
		}
	case "del":
		if link, ok := s.links[rec.ID]; ok {
			delete(s.links, rec.ID)
			if s.byURL[link.URL] == rec.ID {
				delete(s.byURL, link.URL)
			}
		}
	}
}

func (s *Store) write(rec record, durable bool) error {
	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if _, err := s.file.Write(append(line, '\n')); err != nil {
		return err
	}
	if durable {
		return s.file.Sync()
	}
	return nil
}

func ValidateURL(raw string) error {
	if raw == "" || len(raw) > MaxURLLength {
		return ErrInvalidURL
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return ErrInvalidURL
	}
	return nil
}

func ValidateAlias(alias string) error {
	if !aliasPattern.MatchString(alias) || reserved[alias] {
		return ErrInvalidAlias
	}
	return nil
}

func (s *Store) expired(link *Link) bool {
	return !link.Expires.IsZero() && !s.clock().Before(link.Expires)
}

func (s *Store) Create(rawURL, alias string, ttl time.Duration) (Link, bool, error) {
	if err := ValidateURL(rawURL); err != nil {
		return Link{}, false, err
	}
	if ttl < 0 {
		return Link{}, false, ErrNegativeTTL
	}
	if alias != "" {
		if err := ValidateAlias(alias); err != nil {
			return Link{}, false, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if alias == "" && ttl == 0 {
		if id, ok := s.byURL[rawURL]; ok {
			if existing := s.links[id]; existing != nil && existing.Expires.IsZero() {
				return *existing, false, nil
			}
		}
	}
	now := s.clock().UTC().Truncate(time.Second)
	link := &Link{URL: rawURL, Created: now, Custom: alias != ""}
	if ttl > 0 {
		link.Expires = now.Add(ttl)
	}
	rec := record{Op: "put", URL: rawURL, Created: now.Unix(), Custom: link.Custom}
	if !link.Expires.IsZero() {
		rec.Expires = link.Expires.Unix()
	}
	if alias != "" {
		if existing, taken := s.links[alias]; taken && !s.expired(existing) {
			return Link{}, false, ErrAliasTaken
		}
		link.ID = alias
	} else {
		next := s.seq + 1
		for {
			if _, taken := s.links[Encode(next)]; !taken {
				break
			}
			next++
		}
		link.ID = Encode(next)
		rec.Seq = next
	}
	rec.ID = link.ID
	if err := s.write(rec, true); err != nil {
		return Link{}, false, err
	}
	s.apply(rec)
	return *s.links[link.ID], true, nil
}

func (s *Store) Get(id string) (Link, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	link, ok := s.links[id]
	if !ok {
		return Link{}, ErrNotFound
	}
	if s.expired(link) {
		return *link, ErrExpired
	}
	return *link, nil
}

func (s *Store) Visit(id string) (Link, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	link, ok := s.links[id]
	if !ok {
		return Link{}, ErrNotFound
	}
	if s.expired(link) {
		return *link, ErrExpired
	}
	if err := s.write(record{Op: "hit", ID: id}, false); err != nil {
		return Link{}, err
	}
	link.Hits++
	return *link, nil
}

func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.links[id]; !ok {
		return ErrNotFound
	}
	rec := record{Op: "del", ID: id}
	if err := s.write(rec, true); err != nil {
		return err
	}
	s.apply(rec)
	return nil
}

func (s *Store) List(offset, limit int) []Link {
	s.mu.Lock()
	defer s.mu.Unlock()
	all := make([]Link, 0, len(s.links))
	for _, link := range s.links {
		all = append(all, *link)
	}
	sort.Slice(all, func(i, j int) bool {
		if !all[i].Created.Equal(all[j].Created) {
			return all[i].Created.After(all[j].Created)
		}
		return all[i].ID < all[j].ID
	})
	if offset < 0 {
		offset = 0
	}
	if offset >= len(all) {
		return []Link{}
	}
	all = all[offset:]
	if limit > 0 && limit < len(all) {
		all = all[:limit]
	}
	return all
}

func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.links)
}

func (s *Store) Compact() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tmpPath := s.path + ".tmp"
	tmp, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, err
	}
	dropped := 0
	keep := make(map[string]*Link, len(s.links))
	writer := bufio.NewWriter(tmp)
	ids := make([]string, 0, len(s.links))
	for id := range s.links {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	seqLine, _ := json.Marshal(record{Op: "seq", Seq: s.seq})
	writer.Write(append(seqLine, '\n'))
	for _, id := range ids {
		link := s.links[id]
		if s.expired(link) {
			dropped++
			continue
		}
		keep[id] = link
		rec := record{Op: "put", ID: id, URL: link.URL, Created: link.Created.Unix(), Hits: link.Hits, Custom: link.Custom}
		if !link.Expires.IsZero() {
			rec.Expires = link.Expires.Unix()
		}
		if !link.Custom {
			if n, err := Decode(id); err == nil {
				rec.Seq = n
			}
		}
		line, _ := json.Marshal(rec)
		writer.Write(append(line, '\n'))
	}
	if err := writer.Flush(); err != nil {
		tmp.Close()
		return 0, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return 0, err
	}
	tmp.Close()
	if err := s.file.Close(); err != nil {
		return 0, err
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		return 0, err
	}
	file, err := os.OpenFile(s.path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return 0, err
	}
	s.file = file
	s.links = keep
	s.byURL = make(map[string]string)
	for id, link := range keep {
		if !link.Custom && link.Expires.IsZero() {
			s.byURL[link.URL] = id
		}
	}
	return dropped, nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if err := s.file.Sync(); err != nil {
		s.file.Close()
		return err
	}
	return s.file.Close()
}
