package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeStore struct {
	memes   []Meme
	lastF   Filter
	created []Meme
}

func (f *fakeStore) List(_ context.Context, flt Filter) ([]Meme, error) {
	f.lastF = flt
	return f.memes, nil
}
func (f *fakeStore) Get(_ context.Context, id int) (Meme, error) {
	for _, m := range f.memes {
		if m.ID == id {
			return m, nil
		}
	}
	return Meme{}, ErrNotFound
}
func (f *fakeStore) Countries(context.Context) ([]Country, error) {
	return []Country{{Code: "US", Name: "United States", Count: 1}}, nil
}
func (f *fakeStore) Create(_ context.Context, m Meme) (Meme, error) {
	m.ID = 100
	f.created = append(f.created, m)
	return m, nil
}
func (f *fakeStore) Ping(context.Context) error { return nil }

type mapCache struct {
	data        map[string][]byte
	invalidated int
}

func (c *mapCache) Get(_ context.Context, k string) ([]byte, bool) { v, ok := c.data[k]; return v, ok }
func (c *mapCache) Set(_ context.Context, k string, v []byte)      { c.data[k] = v }
func (c *mapCache) Invalidate(context.Context)                     { c.invalidated++; c.data = map[string][]byte{} }

func setup() (*fakeStore, *mapCache, http.Handler) {
	st := &fakeStore{memes: []Meme{{ID: 1, Title: "Doge", CountryCode: "JP", CountryName: "Japan", Year: 2013, Status: "classic"}}}
	c := &mapCache{data: map[string][]byte{}}
	return st, c, newServer(st, c, "secret").routes()
}

func do(h http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestListMemesPassesFilterAndCaches(t *testing.T) {
	st, _, h := setup()

	rec := do(h, "GET", "/api/memes?country=jp&q=doge&sort=old", "", "")
	if rec.Code != 200 || rec.Header().Get("X-Cache") != "MISS" {
		t.Fatalf("first call: code %d, X-Cache %q", rec.Code, rec.Header().Get("X-Cache"))
	}
	if st.lastF != (Filter{Country: "JP", Query: "doge", Oldest: true}) {
		t.Fatalf("unexpected filter %+v", st.lastF)
	}
	var got []Meme
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got) != 1 {
		t.Fatalf("body %s", rec.Body)
	}

	rec = do(h, "GET", "/api/memes?country=jp&q=doge&sort=old", "", "")
	if rec.Header().Get("X-Cache") != "HIT" {
		t.Fatalf("second call should hit cache, got %q", rec.Header().Get("X-Cache"))
	}
}

func TestListMemesRejectsBadCountry(t *testing.T) {
	_, _, h := setup()
	if rec := do(h, "GET", "/api/memes?country=USA", "", ""); rec.Code != 400 {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestGetMeme(t *testing.T) {
	_, _, h := setup()
	if rec := do(h, "GET", "/api/memes/1", "", ""); rec.Code != 200 {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if rec := do(h, "GET", "/api/memes/999", "", ""); rec.Code != 404 {
		t.Fatalf("want 404, got %d", rec.Code)
	}
	if rec := do(h, "GET", "/api/memes/abc", "", ""); rec.Code != 400 {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestCreateMeme(t *testing.T) {
	const body = `{"title":"Test","country_code":"ru","country_name":"Russia","year":2024,"status":"trend"}`

	t.Run("without token", func(t *testing.T) {
		st, _, h := setup()
		if rec := do(h, "POST", "/api/memes", body, ""); rec.Code != 401 {
			t.Fatalf("want 401, got %d", rec.Code)
		}
		if rec := do(h, "POST", "/api/memes", body, "wrong"); rec.Code != 401 {
			t.Fatalf("want 401, got %d", rec.Code)
		}
		if len(st.created) != 0 {
			t.Fatal("meme must not be created")
		}
	})

	t.Run("invalid data", func(t *testing.T) {
		_, _, h := setup()
		bad := strings.Replace(body, `"trend"`, `"viral"`, 1)
		if rec := do(h, "POST", "/api/memes", bad, "secret"); rec.Code != 400 {
			t.Fatalf("want 400, got %d", rec.Code)
		}
	})

	t.Run("ok", func(t *testing.T) {
		st, c, h := setup()
		rec := do(h, "POST", "/api/memes", body, "secret")
		if rec.Code != 201 {
			t.Fatalf("want 201, got %d: %s", rec.Code, rec.Body)
		}
		if len(st.created) != 1 || st.created[0].CountryCode != "RU" {
			t.Fatalf("created %+v", st.created)
		}
		if c.invalidated != 1 {
			t.Fatal("cache must be invalidated after create")
		}
	})
}

func TestCreateDisabledWithoutAdminToken(t *testing.T) {
	st := &fakeStore{}
	h := newServer(st, noCache{}, "").routes()
	rec := do(h, "POST", "/api/memes", `{}`, "")
	if rec.Code != 401 {
		t.Fatalf("want 401, got %d", rec.Code)
	}
}

func TestHealthAndIndex(t *testing.T) {
	_, _, h := setup()
	for _, p := range []string{"/healthz", "/readyz", "/"} {
		if rec := do(h, "GET", p, "", ""); rec.Code != 200 {
			t.Fatalf("%s: want 200, got %d", p, rec.Code)
		}
	}
}
