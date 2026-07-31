// Pilo is a tiny, self-hosted PaaS for deploying static (HTML/CSS/JS)
// tools straight from GitHub repos onto a local path, e.g.
// http://pilo.local:8080/my-tool/. Single binary, SQLite storage,
// pure-Go — built to run comfortably on a Raspberry Pi.
package main

import (
	"log"
	"net/http"
	"os"
	"path/filepath"

	"pilo/internal/deploy"
	"pilo/internal/server"
	"pilo/internal/store"
)

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	dataDir := getenv("PILO_DATA_DIR", "./data")
	port := getenv("PILO_PORT", "8080")
	baseURL := getenv("PILO_BASE_URL", "http://localhost:"+port)

	st, err := store.New(filepath.Join(dataDir, "paas.db"))
	if err != nil {
		log.Fatalf("loading store: %v", err)
	}
	defer st.Close()

	dep := deploy.New(filepath.Join(dataDir, "repos"))

	router := server.NewRouter(nil) // admin handler wired in below
	handlers := server.NewHandlers(st, dep, router, baseURL)

	adminMux := handlers.Mux()
	router.SetAdmin(adminMux)

	// Re-register routes for every project already known to the store,
	// so a restart doesn't take existing tools offline.
	for _, p := range st.All() {
		root := dep.PathFor(p.Slug)
		if p.Subdir != "" {
			root = root + "/" + p.Subdir
		}
		router.RegisterProject(p.Slug, http.FileServer(http.Dir(root)))
		log.Printf("registered existing project: /%s/ -> %s", p.Slug, root)
	}

	addr := ":" + port
	log.Printf("pilo listening on %s (data dir: %s)", addr, dataDir)
	if err := http.ListenAndServe(addr, router); err != nil {
		log.Fatal(err)
	}
}
