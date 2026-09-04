package dtrpg

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchCoverSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("fake-png-bytes"))
	}))
	defer srv.Close()

	cover, err := FetchCover(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("FetchCover: %v", err)
	}
	if cover.ContentType != "image/png" || string(cover.Data) != "fake-png-bytes" {
		t.Errorf("unexpected cover: %+v", cover)
	}
}

func TestFetchCoverEmptyURL(t *testing.T) {
	if _, err := FetchCover(context.Background(), nil, ""); err == nil {
		t.Fatal("expected an error for an empty cover URL")
	}
}

func TestFetchCoverNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	if _, err := FetchCover(context.Background(), srv.Client(), srv.URL); err == nil {
		t.Fatal("expected an error for a 404 cover")
	}
}

func TestFetchCoverRejectsUnsupportedContentType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write([]byte("not an image"))
	}))
	defer srv.Close()

	_, err := FetchCover(context.Background(), srv.Client(), srv.URL)
	if err == nil {
		t.Fatal("expected an error for a non-image content type")
	}
}
