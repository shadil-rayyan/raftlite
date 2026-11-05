// Package traditional implements the centralized policy server: one Go
// process storing policies in a local SQLite file, with a monotonic
// revision counter. This is "Approach 1" in the comparison.
package traditional

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/shadil-rayyan/traditional-vs-raftlite-evaluation/internal/policy"
	_ "modernc.org/sqlite"
)

// Server is the central policy server.
type Server struct {
	db  *sql.DB
	log *slog.Logger
}

// Open opens (or creates) the SQLite database and ensures the schema.
func Open(dbPath string, log *slog.Logger) (*Server, error) {
	if log == nil {
		log = slog.Default()
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// modernc/sqlite: serialize writes on this connection pool.
	db.SetMaxOpenConns(1)
	schema := `
CREATE TABLE IF NOT EXISTS policies (
  id     TEXT PRIMARY KEY,
  ip     TEXT NOT NULL,
  action TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS meta (
  key   TEXT PRIMARY KEY,
  value INTEGER NOT NULL
);
INSERT OR IGNORE INTO meta(key, value) VALUES ('revision', 0);
`
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	return &Server{db: db, log: log}, nil
}

// Close releases the database.
func (s *Server) Close() error { return s.db.Close() }

// Handler returns the HTTP routes of the central server.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "system": "traditional"})
	})
	mux.HandleFunc("GET /v1/policies", s.handleList)
	mux.HandleFunc("POST /v1/policies", s.handleCreate)
	mux.HandleFunc("DELETE /v1/policies/{id}", s.handleDelete)
	return mux
}

func (s *Server) handleList(w http.ResponseWriter, _ *http.Request) {
	// One transaction so the list and the revision are consistent.
	tx, err := s.db.Begin()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	defer tx.Rollback()

	var rev uint64
	if err := tx.QueryRow(`SELECT value FROM meta WHERE key='revision'`).Scan(&rev); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	rows, err := tx.Query(`SELECT id, ip, action FROM policies ORDER BY id`)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	defer rows.Close()
	pols := []policy.Policy{}
	for rows.Next() {
		var p policy.Policy
		if err := rows.Scan(&p.ID, &p.IP, &p.Action); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		pols = append(pols, p)
	}
	writeJSON(w, http.StatusOK, policy.ListResponse{Policies: pols, Revision: rev})
}

type createRequest struct {
	ID     string `json:"id"`
	IP     string `json:"ip"`
	Action string `json:"action"`
}

func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
		return
	}
	req.IP = strings.TrimSpace(req.IP)
	if req.IP == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "ip is required"})
		return
	}
	if req.ID == "" {
		req.ID = fmt.Sprintf("pol-%d", time.Now().UnixNano())
	}
	req.Action = policy.NormalizeAction(req.Action)
	if req.Action == "" {
		req.Action = "BLOCK"
	}

	tx, err := s.db.Begin()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`INSERT INTO policies(id, ip, action) VALUES(?,?,?)
		 ON CONFLICT(id) DO UPDATE SET ip=excluded.ip, action=excluded.action`,
		req.ID, req.IP, req.Action); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	rev, err := bumpRevision(tx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if err := tx.Commit(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	s.log.Info("policy created", "id", req.ID, "ip", req.IP, "action", req.Action, "revision", rev)
	writeJSON(w, http.StatusCreated, map[string]any{
		"status":    "committed",
		"policy_id": req.ID,
		"revision":  rev,
	})
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	tx, err := s.db.Begin()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	defer tx.Rollback()
	res, err := tx.Exec(`DELETE FROM policies WHERE id=?`, id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no such policy"})
		return
	}
	rev, err := bumpRevision(tx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if err := tx.Commit(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "committed", "policy_id": id, "revision": rev})
}

// bumpRevision increments the revision counter inside a transaction and
// returns the new value. Every write bumps it exactly once, so gateways
// can detect any change by comparing revisions.
func bumpRevision(tx *sql.Tx) (uint64, error) {
	if _, err := tx.Exec(`UPDATE meta SET value = value + 1 WHERE key='revision'`); err != nil {
		return 0, err
	}
	var rev uint64
	if err := tx.QueryRow(`SELECT value FROM meta WHERE key='revision'`).Scan(&rev); err != nil {
		return 0, err
	}
	return rev, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
