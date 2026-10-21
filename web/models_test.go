package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchModels(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"id": "glm-4"}, {"id": "kimi-k2"}, {"id": ""}},
		})
	}))
	defer srv.Close()
	models, err := FetchModels(srv.URL+"/v1", "sk-test-key", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer sk-test-key" {
		t.Fatalf("Authorization = %q, want Bearer sk-test-key", gotAuth)
	}
	if len(models) != 2 || models[0] != "glm-4" || models[1] != "kimi-k2" {
		t.Fatalf("models = %v", models)
	}
}

func TestFetchModelsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer srv.Close()
	if _, err := FetchModels(srv.URL+"/v1", "", srv.Client()); err == nil {
		t.Fatal("expected error on upstream failure")
	}
}
