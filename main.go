package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"Chirpy/internal/database"

	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
)

// User represents a user in the API responses
type User struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Email     string    `json:"email"`
}

// apiConfig holds our application state
type apiConfig struct {
	fileserverHits atomic.Int32
	db             *database.Queries
	platform       string
}

func main() {
	// Load environment variables from .env file
	err := godotenv.Load()
	if err != nil {
		fmt.Println("Warning: Error loading .env file:", err)
	}

	// Get database connection string from environment
	dbURL := os.Getenv("DB_URL")
	if dbURL == "" {
		panic("DB_URL environment variable is not set")
	}

	// Get platform from environment
	platform := os.Getenv("PLATFORM")
	if platform == "" {
		platform = "production" // default to production
	}

	// Open database connection
	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		panic(fmt.Sprintf("Error opening database: %v", err))
	}
	defer db.Close()

	// Test database connection
	err = db.Ping()
	if err != nil {
		panic(fmt.Sprintf("Error connecting to database: %v", err))
	}
	fmt.Println("Successfully connected to database!")

	// Create database queries instance
	dbQueries := database.New(db)

	// Initialize our API config
	apiCfg := &apiConfig{
		db:       dbQueries,
		platform: platform,
	}

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

	// 4. Add admin metrics endpoint at /admin/metrics (GET only)
	mux.HandleFunc("/admin/metrics", methodGET(apiCfg.adminMetricsHandler))

	// 5. Add admin reset endpoint at /admin/reset (POST only)
	mux.HandleFunc("/admin/reset", methodPOST(apiCfg.resetHandler))

	// 6. Add new API endpoint for validating chirps
	mux.HandleFunc("/api/validate_chirp", methodPOST(apiCfg.validateChirpHandler))

	// 7. Add user creation endpoint at /api/users (POST only)
	mux.HandleFunc("/api/users", methodPOST(apiCfg.createUserHandler))

	server := &http.Server{
		Handler: mux,
		Addr:    ":8080",
	}

	fmt.Println("Server starting on :8080...")
	err = server.ListenAndServe()
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

// adminMetricsHandler returns the current hit count as HTML
func (cfg *apiConfig) adminMetricsHandler(w http.ResponseWriter, r *http.Request) {
	// Get the current count atomically
	hits := cfg.fileserverHits.Load()

	// Set Content-Type to HTML
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)

	// Create HTML response using fmt.Sprintf
	html := fmt.Sprintf(`<html>
  <body>
    <h1>Welcome, Chirpy Admin</h1>
    <p>Chirpy has been visited %d times!</p>
  </body>
</html>`, hits)

	w.Write([]byte(html))
}

// resetHandler resets the hit counter to 0 and deletes all users (dev only)
func (cfg *apiConfig) resetHandler(w http.ResponseWriter, r *http.Request) {
	// Only allow in development environment
	if cfg.platform != "dev" {
		respondWithError(w, http.StatusForbidden, "Forbidden")
		return
	}

	// Reset the hit counter to 0
	cfg.fileserverHits.Store(0)

	// Delete all users from database
	err := cfg.db.DeleteAllUsers(r.Context())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error resetting database")
		return
	}

	respondWithJSON(w, http.StatusOK, map[string]string{
		"message": "Database reset successfully",
	})
}

// createUserHandler handles POST /api/users
func (cfg *apiConfig) createUserHandler(w http.ResponseWriter, r *http.Request) {
	// Define the expected JSON structure
	type parameters struct {
		Email string `json:"email"`
	}

	// Decode the JSON request body
	decoder := json.NewDecoder(r.Body)
	params := parameters{}
	err := decoder.Decode(&params)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}

	// Validate email (basic validation)
	if params.Email == "" {
		respondWithError(w, http.StatusBadRequest, "Email is required")
		return
	}

	// Create user in database
	dbUser, err := cfg.db.CreateUser(r.Context(), params.Email)
	if err != nil {
		// Check if it's a duplicate email error
		if strings.Contains(err.Error(), "duplicate key") {
			respondWithError(w, http.StatusConflict, "Email already exists")
			return
		}
		respondWithError(w, http.StatusInternalServerError, "Error creating user")
		return
	}

	// Convert database user to API user
	user := User{
		ID:        dbUser.ID.String(),
		CreatedAt: dbUser.CreatedAt,
		UpdatedAt: dbUser.UpdatedAt,
		Email:     dbUser.Email,
	}

	// Return created user
	respondWithJSON(w, http.StatusCreated, user)
}

// validateChirpHandler handles POST /api/validate_chirp
func (cfg *apiConfig) validateChirpHandler(w http.ResponseWriter, r *http.Request) {
	// Define the expected JSON structure
	type parameters struct {
		Body string `json:"body"`
	}

	// Decode the JSON request body
	decoder := json.NewDecoder(r.Body)
	params := parameters{}
	err := decoder.Decode(&params)
	if err != nil {
		// If JSON is invalid
		respondWithError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}

	// Validate chirp length (140 characters or less)
	if len(params.Body) > 140 {
		respondWithError(w, http.StatusBadRequest, "Chirp is too long")
		return
	}

	// Clean the chirp (replace profane words)
	cleanedBody := cleanChirp(params.Body)

	// Return cleaned chirp
	respondWithJSON(w, http.StatusOK, map[string]string{
		"cleaned_body": cleanedBody,
	})
}

// cleanChirp replaces profane words with ****
func cleanChirp(body string) string {
	// List of profane words (lowercase)
	profaneWords := map[string]bool{
		"kerfuffle": true,
		"sharbert":  true,
		"fornax":    true,
	}

	// Split the body into words
	words := strings.Split(body, " ")

	// Process each word
	for i, word := range words {
		// Convert to lowercase for comparison (but preserve original case for punctuation)
		lowerWord := strings.ToLower(word)

		// Check if the word (without punctuation) is profane
		// We need to handle punctuation at the end of words
		cleanWord := lowerWord
		punctuation := ""

		// Check if word ends with punctuation
		if len(word) > 0 {
			lastChar := word[len(word)-1]
			if lastChar == '!' || lastChar == '?' || lastChar == '.' || lastChar == ',' {
				cleanWord = strings.ToLower(word[:len(word)-1])
				punctuation = string(lastChar)
			}
		}

		// If the clean word is profane, replace it
		if profaneWords[cleanWord] {
			words[i] = "****" + punctuation
		}
	}

	// Join the words back together
	return strings.Join(words, " ")
}

// Helper functions for JSON responses

// respondWithError sends an error response as JSON
func respondWithError(w http.ResponseWriter, code int, msg string) {
	respondWithJSON(w, code, map[string]string{"error": msg})
}

// respondWithJSON sends a JSON response
func respondWithJSON(w http.ResponseWriter, code int, payload interface{}) {
	// Marshal the payload to JSON
	response, err := json.Marshal(payload)
	if err != nil {
		// If we can't marshal the response, something is seriously wrong
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Internal Server Error"))
		return
	}

	// Set headers and write response
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	w.Write(response)
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
