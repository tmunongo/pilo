// Pilo is a tiny, self-hosted PaaS for deploying static (HTML/CSS/JS)
// tools straight from GitHub repos onto a local path, e.g.
// http://pilo.local:8080/projects/my-username/my-tool/. Single binary, SQLite storage,
// pure-Go — built to run comfortably on a Raspberry Pi.
package main

import (
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

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

	// Auto-provision initial account on first boot if provided via environment.
	adminEmail := getenv("PILO_ADMIN_EMAIL", getenv("PILO_EMAIL", ""))
	adminPassword := getenv("PILO_ADMIN_PASSWORD", getenv("PILO_PASSWORD", ""))
	if adminEmail != "" && adminPassword != "" {
		if _, err := st.GetUserByEmail(adminEmail); errors.Is(err, store.ErrUserNotFound) {
			if len(adminPassword) < 8 {
				log.Printf("auth: admin password must be at least 8 characters long")
			} else if _, err := st.CreateUser(adminEmail, adminPassword); err != nil {
				log.Printf("auth: failed to auto-provision initial account for %s: %v", adminEmail, err)
			} else {
				log.Printf("auth: provisioned initial account %s from environment", adminEmail)
			}
		}
	}

	dep := deploy.New(filepath.Join(dataDir, "repos"))

	if err := dep.MigrateReleases(st.All()); err != nil {
		log.Fatalf("migrating project releases: %v", err)
	}

	recoverUploadProjects(st, dep)

	router := server.NewRouter(nil) // admin handler wired in below
	handlers := server.NewHandlers(st, dep, router, baseURL)
	handlers.Port = port
	handlers.Version = getenv("PILO_VERSION", "v0.1.0")

	// PILO_DISABLE_SIGNUPS controls registration.
	// If unset, it defaults to auto: signups open if no accounts exist, closed once an account exists.
	// Any value other than the recognized opt-in strings ("false", "0", "no") is treated as disabled.
	// Comparisons are case- and whitespace-insensitive.
	if disableSignupsEnv := os.Getenv("PILO_DISABLE_SIGNUPS"); disableSignupsEnv != "" {
		normalized := strings.TrimSpace(strings.ToLower(disableSignupsEnv))
		enabled := normalized == "false" || normalized == "0" || normalized == "no"
		disabled := !enabled
		handlers.SignupsDisabled = &disabled
	}

	adminMux := handlers.Mux()
	router.SetAdmin(adminMux)

	// Re-register routes for every project already known to the store,
	// so a restart doesn't take existing tools offline.
	for _, p := range st.All() {
		if err := deploy.ValidateSubdir(p.Subdir); err != nil {
			log.Printf("skipping project %s: %v", p.Slug, err)
			continue
		}
		root := dep.PathFor(p.CheckoutKey())
		if p.Subdir != "" {
			root = root + "/" + p.Subdir
		}
		router.RegisterProject(p.CheckoutKey(), dep.SiteHandler(p.CheckoutKey()))
		log.Printf("registered existing project: %s -> %s", p.PublicPath(), root)
	}

	addr := ":" + port
	log.Printf("pilo listening on %s (data dir: %s)", addr, dataDir)
	if err := http.ListenAndServe(addr, router); err != nil {
		log.Fatal(err)
	}
}

func recoverUploadProjects(st *store.Store, dep *deploy.Deployer) {
	for _, p := range st.All() {
		// Upload publication switches the filesystem pointer before updating the
		// database. Recover that metadata if a process stopped between those steps.
		if p.IsUpload() {
			commit, subdir, activeErr := dep.ActiveRelease(p.CheckoutKey())
			if activeErr == nil {
				if p.LastCommit != commit || p.Subdir != subdir || p.Status != store.StatusLive {
					p.LastCommit, p.Subdir, p.Status, p.LastError = commit, subdir, store.StatusLive, ""
					if err := st.Put(p); err != nil {
						log.Printf("recovering upload %s: %v", p.Slug, err)
					}
				}
			} else if p.Status == store.StatusCloning {
				p.Status, p.LastError = store.StatusError, "Upload interrupted. Upload a new version to deploy."
				if err := st.Put(p); err != nil {
					log.Printf("recovering interrupted upload %s: %v", p.Slug, err)
				}
			}
		}
	}
}
