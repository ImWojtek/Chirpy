package main

import (
	"fmt"
	"net/http"
	"sync/atomic"
)

// apiConfig holds our application state
type apiConfig struct {
	fileserverHits atomic.Int32
}

func main() {
	// Initialize our API config
	apiCfg := &apiConfig{}

	mux := http.NewServeMux()

	// 1. Add the readiness endpoint at /api/healthz (GET only)
	mux.HandleFunc("/api/healthz", methodGET(readinessHandler))

	// 2. Update the fileserver to use the /app/ path
	// Create a file server for the current directory
	fileServer := http.FileServer(http.Dir("."))
	// Strip the /app prefix before passing to the file server
	appHandler := http.StripPrefix("/app", fileServer)
	// Wrap the file server with metrics middleware
	mux.Handle("/app/", apiCfg.middlewareMetricsInc(appHandler))
	// 3. Create a separate file server for assets
	assetsHandler := http.FileServer(http.Dir("./assets"))
	mux.Handle("/assets/", http.StripPrefix("/assets", assetsHandler))

	// 4. Add metrics endpoint at /api/metrics (GET only)
	mux.HandleFunc("/api/metrics", methodGET(apiCfg.metricsHandler))

	// 5. Add reset endpoint at /api/reset (POST only)
	mux.HandleFunc("/api/reset", methodPOST(apiCfg.resetHandler))

	server := &http.Server{
		Handler: mux,
		Addr:    ":8080",
	}

	err := server.ListenAndServe()
	if err != nil {
		panic(err)
	}
}

// readinessHandler handles the /api/healthz endpoint
func readinessHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

// middlewareMetricsInc increments the hit counter for each request
func (cfg *apiConfig) middlewareMetricsInc(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Increment the counter atomically
		cfg.fileserverHits.Add(1)
		// Call the next handler
		next.ServeHTTP(w, r)
	})
}

// metricsHandler returns the current hit count
func (cfg *apiConfig) metricsHandler(w http.ResponseWriter, r *http.Request) {
	// Get the current count atomically
	hits := cfg.fileserverHits.Load()

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)

	// Format the response
	response := fmt.Sprintf("Hits: %d", hits)
	w.Write([]byte(response))
}

// resetHandler resets the hit counter to 0
func (cfg *apiConfig) resetHandler(w http.ResponseWriter, r *http.Request) {
	// Reset the counter to 0
	cfg.fileserverHits.Store(0)

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Hits counter reset to 0"))
}

// methodGET wraps a handler to only allow GET requests
func methodGET(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		next(w, r)
	}
}

// methodPOST wraps a handler to only allow POST requests
func methodPOST(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		next(w, r)
	}
}
