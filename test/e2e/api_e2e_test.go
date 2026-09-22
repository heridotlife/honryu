//go:build e2e

// Package e2e exercises the whole API stack end to end: a real HTTP client over
// a real network socket → router → application services → the MySQL adapter →
// a real MySQL container. It is the highest-fidelity Phase 0 test.
package e2e_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/heridotlife/honryu/internal/adapters/httpapi"
	mysqladapter "github.com/heridotlife/honryu/internal/adapters/repo/mysql"
	"github.com/heridotlife/honryu/internal/app/projectapp"
	"github.com/heridotlife/honryu/test/dbtest"
)

func TestAPI_ProjectsEndToEnd(t *testing.T) {
	db := dbtest.StartMySQL(t)
	repo := mysqladapter.NewProjectRepository(db)
	svc := projectapp.NewService(repo)

	router := httpapi.NewRouter(httpapi.Deps{
		Projects:      svc,
		DefaultOwners: []string{"honryu"},
	})
	srv := httptest.NewServer(router)
	defer srv.Close()
	client := srv.Client()

	// Health check.
	var health map[string]string
	getJSON(t, client, srv.URL+"/healthz", http.StatusOK, &health)
	if health["status"] != "ok" {
		t.Fatalf("healthz status = %q, want ok", health["status"])
	}

	// Projects list starts without OUR project (the 0079-0084 marketplace
	// seed migrations pre-populate tenants/projects in every fresh DB).
	var before []map[string]any
	getJSON(t, client, srv.URL+"/api/projects", http.StatusOK, &before)
	for _, p := range before {
		if p["name"] == "web-api" {
			t.Fatalf("initial projects already contain web-api: %v", before)
		}
	}

	// Seed a project through the real service → MySQL write path.
	created, err := svc.Create(context.Background(), "web-api", "honryu", "77")
	if err != nil {
		t.Fatalf("seed create: %v", err)
	}

	// The read path over HTTP now returns it.
	var listed []struct {
		ID    int64  `json:"id"`
		Name  string `json:"name"`
		Owner string `json:"owner"`
		SID   string `json:"sid"`
	}
	getJSON(t, client, srv.URL+"/api/projects", http.StatusOK, &listed)
	var ours *struct {
		ID    int64  `json:"id"`
		Name  string `json:"name"`
		Owner string `json:"owner"`
		SID   string `json:"sid"`
	}
	for i := range listed {
		if listed[i].Name == "web-api" {
			ours = &listed[i]
		}
	}
	if ours == nil || ours.SID != "77" {
		t.Fatalf("projects = %+v, want our web-api with sid 77", listed)
	}

	// Fetch the single project by ID.
	var single map[string]any
	getJSON(t, client, srv.URL+"/api/projects/"+strconv.FormatInt(created.ID, 10), http.StatusOK, &single)
	if single["name"] != "web-api" {
		t.Fatalf("single project name = %v, want web-api", single["name"])
	}

	// Unknown ID is a 404.
	getJSON(t, client, srv.URL+"/api/projects/987654", http.StatusNotFound, nil)
}

func getJSON(t *testing.T, client *http.Client, url string, wantStatus int, out any) {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != wantStatus {
		t.Fatalf("GET %s status = %d, want %d", url, resp.StatusCode, wantStatus)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("decode %s: %v", url, err)
		}
	}
}
