// Package storage defines the storage abstraction for persisting LLM request
// and response data. Implementations include SQLite (embedded, dev-friendly)
// and PostgreSQL (production).
package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/chingjustwe/llm-interceptor/internal/types"
	_ "modernc.org/sqlite"
)

// SQLiteBackend implements the Backend interface using an embedded SQLite
// database. It is the default storage engine for development and single-node
// deployments.
type SQLiteBackend struct {
	db         *sql.DB
	compressor CompressionConfig
}

// NewSQLite opens (or creates) a SQLite database at the given path and
// initializes the requests table and indexes if they do not exist.
func NewSQLite(path string, compressor CompressionConfig) (*SQLiteBackend, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS requests (
			id TEXT PRIMARY KEY,
			session_id TEXT,
			model TEXT,
			method TEXT,
			path TEXT,
			request_body TEXT,
			response_body TEXT,
			input_tokens INTEGER DEFAULT 0,
			output_tokens INTEGER DEFAULT 0,
			cache_read_tokens INTEGER DEFAULT 0,
			cache_creation_tokens INTEGER DEFAULT 0,
			duration_ms INTEGER,
			status_code INTEGER,
			created_at INTEGER
		);
		CREATE INDEX IF NOT EXISTS idx_requests_session ON requests(session_id);
		CREATE INDEX IF NOT EXISTS idx_requests_created ON requests(created_at);

		CREATE TABLE IF NOT EXISTS api_keys (
			id TEXT PRIMARY KEY,
			key_hash TEXT NOT NULL,
			key_prefix TEXT NOT NULL UNIQUE,
			name TEXT NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_api_keys_prefix ON api_keys(key_prefix);

		CREATE TABLE IF NOT EXISTS runtime_config (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			updated_at INTEGER NOT NULL,
			updated_by TEXT NOT NULL DEFAULT ''
		);

		CREATE TABLE IF NOT EXISTS audit_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			action TEXT NOT NULL,
			target_key TEXT NOT NULL,
			old_value TEXT,
			new_value TEXT,
			performed_by TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_audit_created ON audit_log(created_at);
	`); err != nil {
		db.Close()
		return nil, fmt.Errorf("create table: %w", err)
	}
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		version = 0
	}
	if version < 1 {
		for _, stmt := range []string{
			"ALTER TABLE requests ADD COLUMN system_prompt TEXT",
			"ALTER TABLE requests ADD COLUMN stop_reason TEXT",
			"ALTER TABLE requests ADD COLUMN error_type TEXT",
			"ALTER TABLE requests ADD COLUMN error_message TEXT",
			"ALTER TABLE requests ADD COLUMN ttft_ms INTEGER",
			"ALTER TABLE requests ADD COLUMN temperature REAL",
			"ALTER TABLE requests ADD COLUMN top_p REAL",
			"ALTER TABLE requests ADD COLUMN request_params TEXT",
		} {
			if _, err := db.Exec(stmt); err != nil {
				db.Close()
				return nil, fmt.Errorf("migrate column: %w", err)
			}
		}
		if _, err := db.Exec("CREATE INDEX IF NOT EXISTS idx_requests_stop_reason ON requests(stop_reason)"); err != nil {
			db.Close()
			return nil, fmt.Errorf("create stop_reason index: %w", err)
		}
		if _, err := db.Exec("CREATE INDEX IF NOT EXISTS idx_requests_error_type ON requests(error_type)"); err != nil {
			db.Close()
			return nil, fmt.Errorf("create error_type index: %w", err)
		}
		if _, err := db.Exec("PRAGMA user_version = 1"); err != nil {
			db.Close()
			return nil, fmt.Errorf("set user_version: %w", err)
		}
	}
	if !compressor.Enabled {
		compressor = CompressionConfig{Enabled: false}
	}
	return &SQLiteBackend{db: db, compressor: compressor}, nil
}

// SaveRequest inserts a new LLM request record into the database, including
// metadata, token usage, and the original request/response bodies.
func (s *SQLiteBackend) SaveRequest(ctx context.Context, req *types.StoredRequest) error {
	var (
		systemPrompt  interface{} = nil
		stopReason    interface{} = nil
		errorType     interface{} = nil
		errorMessage  interface{} = nil
		ttftMs        interface{} = nil
		temperature   interface{} = nil
		topP          interface{} = nil
		requestParams interface{} = nil
	)
	if req.SystemPrompt != nil {
		systemPrompt = *req.SystemPrompt
	}
	if req.StopReason != nil {
		stopReason = *req.StopReason
	}
	if req.ErrorType != nil {
		errorType = *req.ErrorType
	}
	if req.ErrorMessage != nil {
		errorMessage = *req.ErrorMessage
	}
	if req.TTFTMs != nil {
		ttftMs = *req.TTFTMs
	}
	if req.Temperature != nil {
		temperature = *req.Temperature
	}
	if req.TopP != nil {
		topP = *req.TopP
	}
	if req.RequestParams != nil {
		requestParams = *req.RequestParams
	}

	// Compress request and response bodies before storage.
	reqBody, err := CompressBody([]byte(req.Request), s.compressor)
	if err != nil {
		return fmt.Errorf("compress request body: %w", err)
	}
	respBody, err := CompressBody([]byte(req.Response), s.compressor)
	if err != nil {
		return fmt.Errorf("compress response body: %w", err)
	}

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO requests (id, session_id, model, method, path, request_body, response_body,
		 input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens,
		 duration_ms, status_code, created_at,
		 system_prompt, stop_reason, error_type, error_message, ttft_ms, temperature, top_p, request_params)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		req.ID, req.SessionID, req.Model, req.Method, req.Path,
		string(reqBody), string(respBody),
		req.Usage.InputTokens, req.Usage.OutputTokens,
		req.Usage.CacheReadTokens, req.Usage.CacheCreationTokens,
		req.DurationMs, req.StatusCode, req.CreatedAt,
		systemPrompt, stopReason, errorType, errorMessage,
		ttftMs, temperature, topP, requestParams,
	)
	if err != nil {
		return fmt.Errorf("save request: %w", err)
	}
	return nil
}

// GetSessionRequests retrieves all requests belonging to a specific session,
// ordered by creation time descending, with pagination via limit and offset.
func (s *SQLiteBackend) GetSessionRequests(ctx context.Context, sessionID string, limit, offset int) ([]types.StoredRequest, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, session_id, model, method, path, request_body, response_body,
		 input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens,
		 duration_ms, status_code, created_at,
		 system_prompt, stop_reason, error_type, error_message, ttft_ms, temperature, top_p, request_params
		 FROM requests WHERE session_id = ? ORDER BY created_at DESC LIMIT ? OFFSET ?`,
		sessionID, limit, offset,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	results := make([]types.StoredRequest, 0)
	for rows.Next() {
		var r types.StoredRequest
		if err := rows.Scan(&r.ID, &r.SessionID, &r.Model, &r.Method, &r.Path,
			&r.Request, &r.Response,
			&r.Usage.InputTokens, &r.Usage.OutputTokens,
			&r.Usage.CacheReadTokens, &r.Usage.CacheCreationTokens,
			&r.DurationMs, &r.StatusCode, &r.CreatedAt,
			&r.SystemPrompt, &r.StopReason, &r.ErrorType, &r.ErrorMessage,
			&r.TTFTMs, &r.Temperature, &r.TopP, &r.RequestParams); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

// QueryRequests retrieves requests matching the given filter criteria (session,
// model, time range) with optional pagination. Results are ordered by creation
// time descending.
func (s *SQLiteBackend) QueryRequests(ctx context.Context, filter types.RequestFilter) ([]types.StoredRequest, error) {
	query := `SELECT id, session_id, model, method, path, request_body, response_body,
		 input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens,
		 duration_ms, status_code, created_at,
		 system_prompt, stop_reason, error_type, error_message, ttft_ms, temperature, top_p, request_params FROM requests`
	var conditions []string
	var args []any

	if filter.SessionID != nil {
		conditions = append(conditions, "session_id LIKE ?")
		args = append(args, "%"+*filter.SessionID+"%")
	}
	if filter.Model != nil {
		conditions = append(conditions, "model LIKE ?")
		args = append(args, "%"+*filter.Model+"%")
	}
	if filter.From != nil {
		conditions = append(conditions, "created_at >= ?")
		args = append(args, *filter.From)
	}
	if filter.To != nil {
		conditions = append(conditions, "created_at <= ?")
		args = append(args, *filter.To)
	}
	if filter.StopReason != nil {
		conditions = append(conditions, "stop_reason = ?")
		args = append(args, *filter.StopReason)
	}
	if filter.ErrorType != nil {
		conditions = append(conditions, "error_type = ?")
		args = append(args, *filter.ErrorType)
	}
	if filter.MinDuration != nil {
		conditions = append(conditions, "duration_ms >= ?")
		args = append(args, *filter.MinDuration)
	}
	if filter.MaxDuration != nil {
		conditions = append(conditions, "duration_ms <= ?")
		args = append(args, *filter.MaxDuration)
	}
	if len(filter.StatusCodes) > 0 {
		placeholders := make([]string, len(filter.StatusCodes))
		for i, sc := range filter.StatusCodes {
			placeholders[i] = "?"
			args = append(args, sc)
		}
		conditions = append(conditions, "status_code IN ("+strings.Join(placeholders, ",")+")")
	}
	if len(conditions) > 0 {
		query += " WHERE " + conditions[0]
		for i := 1; i < len(conditions); i++ {
			query += " AND " + conditions[i]
		}
	}
	query += " ORDER BY created_at DESC"

	// Cursor-based pagination: insert cursor condition before ORDER BY.
	// Cursor is the created_at timestamp of the last item from the previous page.
	if filter.Cursor != nil {
		cursorClause := ""
		if len(conditions) > 0 {
			cursorClause = " AND created_at < ?"
		} else {
			cursorClause = " WHERE created_at < ?"
		}
		query = query[:len(query)-len(" ORDER BY created_at DESC")] + cursorClause + " ORDER BY created_at DESC"
		args = append(args, *filter.Cursor)
	}
	if filter.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, filter.Limit)
	}
	if filter.Offset > 0 && filter.Cursor == nil {
		query += " OFFSET ?"
		args = append(args, filter.Offset)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	results := make([]types.StoredRequest, 0)
	for rows.Next() {
		var r types.StoredRequest
		if err := rows.Scan(&r.ID, &r.SessionID, &r.Model, &r.Method, &r.Path,
			&r.Request, &r.Response,
			&r.Usage.InputTokens, &r.Usage.OutputTokens,
			&r.Usage.CacheReadTokens, &r.Usage.CacheCreationTokens,
			&r.DurationMs, &r.StatusCode, &r.CreatedAt,
			&r.SystemPrompt, &r.StopReason, &r.ErrorType, &r.ErrorMessage,
			&r.TTFTMs, &r.Temperature, &r.TopP, &r.RequestParams); err != nil {
			return nil, err
		}
		// Decompress bodies if compressed.
		if reqBody, err := DecompressBody([]byte(r.Request)); err == nil {
			r.Request = string(reqBody)
		}
		if respBody, err := DecompressBody([]byte(r.Response)); err == nil {
			r.Response = string(respBody)
		}
		results = append(results, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

// SaveAPIKey inserts or updates a managed API key record. The key hash is
// stored using bcrypt, and the prefix allows fast lookup during validation.
func (s *SQLiteBackend) SaveAPIKey(ctx context.Context, key *APIKey) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO api_keys (id, key_hash, key_prefix, name, enabled, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET enabled=excluded.enabled`,
		key.ID, key.KeyHash, key.KeyPrefix, key.Name, key.Enabled, key.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("save api key: %w", err)
	}
	return nil
}

// GetAPIKeyByPrefix retrieves a single API key by its short prefix. Returns
// nil without error if no matching key exists, allowing the caller to
// distinguish "not found" from database errors.
func (s *SQLiteBackend) GetAPIKeyByPrefix(ctx context.Context, prefix string) (*APIKey, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, key_hash, key_prefix, name, enabled, created_at
		 FROM api_keys WHERE key_prefix = ?`, prefix,
	)
	var k APIKey
	err := row.Scan(&k.ID, &k.KeyHash, &k.KeyPrefix, &k.Name, &k.Enabled, &k.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get api key by prefix: %w", err)
	}
	return &k, nil
}

// ListAPIKeys returns all stored API keys ordered by creation time descending.
func (s *SQLiteBackend) ListAPIKeys(ctx context.Context) ([]APIKey, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, key_hash, key_prefix, name, enabled, created_at
		 FROM api_keys ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	defer rows.Close()
	var keys []APIKey
	for rows.Next() {
		var k APIKey
		if err := rows.Scan(&k.ID, &k.KeyHash, &k.KeyPrefix, &k.Name, &k.Enabled, &k.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan api key: %w", err)
		}
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate api keys: %w", err)
	}
	return keys, nil
}

// DisableAPIKey marks an API key as disabled so it can no longer be used for
// authentication. The key record is preserved for audit purposes.
func (s *SQLiteBackend) DisableAPIKey(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE api_keys SET enabled = false WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("disable api key: %w", err)
	}
	return nil
}

// SaveConfig upserts a runtime configuration entry. If the key already exists,
// its value, updated_at, and updated_by are replaced.
func (s *SQLiteBackend) SaveConfig(ctx context.Context, entry *types.ConfigEntry) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO runtime_config (key, value, updated_at, updated_by)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at, updated_by=excluded.updated_by`,
		entry.Key, string(entry.Value), entry.UpdatedAt, entry.UpdatedBy,
	)
	if err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	return nil
}

// GetConfig retrieves a single runtime configuration entry by key. Returns
// nil without error if the key does not exist.
func (s *SQLiteBackend) GetConfig(ctx context.Context, key string) (*types.ConfigEntry, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT key, value, updated_at, updated_by FROM runtime_config WHERE key = ?`, key,
	)
	var entry types.ConfigEntry
	var valueStr string
	err := row.Scan(&entry.Key, &valueStr, &entry.UpdatedAt, &entry.UpdatedBy)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get config: %w", err)
	}
	entry.Value = json.RawMessage(valueStr)
	return &entry, nil
}

// ListConfig returns all runtime configuration entries, ordered by key.
func (s *SQLiteBackend) ListConfig(ctx context.Context) ([]types.ConfigEntry, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT key, value, updated_at, updated_by FROM runtime_config ORDER BY key`,
	)
	if err != nil {
		return nil, fmt.Errorf("list config: %w", err)
	}
	defer rows.Close()
	var entries []types.ConfigEntry
	for rows.Next() {
		var entry types.ConfigEntry
		var valueStr string
		if err := rows.Scan(&entry.Key, &valueStr, &entry.UpdatedAt, &entry.UpdatedBy); err != nil {
			return nil, fmt.Errorf("scan config: %w", err)
		}
		entry.Value = json.RawMessage(valueStr)
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate config: %w", err)
	}
	return entries, nil
}

// DeleteConfig removes a runtime configuration entry by key. It is not an
// error if the key does not exist.
func (s *SQLiteBackend) DeleteConfig(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM runtime_config WHERE key = ?`, key)
	if err != nil {
		return fmt.Errorf("delete config: %w", err)
	}
	return nil
}

// SaveAuditEntry inserts a new audit log entry.
func (s *SQLiteBackend) SaveAuditEntry(ctx context.Context, entry *types.AuditEntry) error {
	result, err := s.db.ExecContext(ctx,
		`INSERT INTO audit_log (action, target_key, old_value, new_value, performed_by, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		entry.Action, entry.TargetKey, entry.OldValue, entry.NewValue, entry.PerformedBy, entry.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("save audit: %w", err)
	}
	id, _ := result.LastInsertId()
	entry.ID = id
	return nil
}

// QueryAuditEntries returns audit log entries ordered by creation time
// descending, with pagination via limit and offset.
func (s *SQLiteBackend) QueryAuditEntries(ctx context.Context, limit, offset int) ([]types.AuditEntry, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, action, target_key, old_value, new_value, performed_by, created_at
		 FROM audit_log ORDER BY created_at DESC LIMIT ? OFFSET ?`, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("query audit: %w", err)
	}
	defer rows.Close()
	var entries []types.AuditEntry
	for rows.Next() {
		var e types.AuditEntry
		if err := rows.Scan(&e.ID, &e.Action, &e.TargetKey, &e.OldValue, &e.NewValue, &e.PerformedBy, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan audit: %w", err)
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate audit: %w", err)
	}
	return entries, nil
}

// Close shuts down the database connection.
func (s *SQLiteBackend) Close() error {
	return s.db.Close()
}
