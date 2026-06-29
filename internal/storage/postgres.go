// Package storage defines the storage abstraction for persisting LLM request
// and response data. Implementations include SQLite (embedded, dev-friendly)
// and PostgreSQL (production).
package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/chingjustwe/llm-interceptor/internal/types"
)

// PostgresBackend implements the Backend interface using a PostgreSQL database.
// It uses a connection pool via pgxpool for concurrent access and production
// deployments.
type PostgresBackend struct {
	pool       *pgxpool.Pool
	compressor CompressionConfig
}

// compile-time check that PostgresBackend satisfies Backend.
var _ Backend = (*PostgresBackend)(nil)

// NewPostgres opens a PostgreSQL connection pool using the given connection
// string, verifies connectivity with a ping, and initializes the requests table
// and indexes if they do not exist.
func NewPostgres(connString string, compressor CompressionConfig) (*PostgresBackend, error) {
	pool, err := pgxpool.New(context.Background(), connString)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	if _, err := pool.Exec(context.Background(), `
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
			created_at TIMESTAMP DEFAULT NOW()
		);
		CREATE INDEX IF NOT EXISTS idx_requests_session ON requests(session_id);
		CREATE INDEX IF NOT EXISTS idx_requests_created ON requests(created_at);

		CREATE TABLE IF NOT EXISTS api_keys (
			id TEXT PRIMARY KEY,
			key_hash TEXT NOT NULL,
			key_prefix TEXT NOT NULL UNIQUE,
			name TEXT NOT NULL,
			enabled BOOLEAN NOT NULL DEFAULT true,
			created_at TIMESTAMP NOT NULL DEFAULT NOW()
		);
		CREATE INDEX IF NOT EXISTS idx_api_keys_prefix ON api_keys(key_prefix);

		CREATE TABLE IF NOT EXISTS runtime_config (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			updated_at TIMESTAMP NOT NULL DEFAULT NOW(),
			updated_by TEXT NOT NULL DEFAULT ''
		);

		CREATE TABLE IF NOT EXISTS audit_log (
			id BIGSERIAL PRIMARY KEY,
			action TEXT NOT NULL,
			target_key TEXT NOT NULL,
			old_value TEXT,
			new_value TEXT,
			performed_by TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL DEFAULT NOW()
		);
		CREATE INDEX IF NOT EXISTS idx_audit_created ON audit_log(created_at);
	`); err != nil {
		pool.Close()
		return nil, fmt.Errorf("create table: %w", err)
	}
	for _, stmt := range []string{
		"ALTER TABLE requests ADD COLUMN IF NOT EXISTS system_prompt TEXT",
		"ALTER TABLE requests ADD COLUMN IF NOT EXISTS stop_reason TEXT",
		"ALTER TABLE requests ADD COLUMN IF NOT EXISTS error_type TEXT",
		"ALTER TABLE requests ADD COLUMN IF NOT EXISTS error_message TEXT",
		"ALTER TABLE requests ADD COLUMN IF NOT EXISTS ttft_ms INTEGER",
		"ALTER TABLE requests ADD COLUMN IF NOT EXISTS temperature DOUBLE PRECISION",
		"ALTER TABLE requests ADD COLUMN IF NOT EXISTS top_p DOUBLE PRECISION",
		"ALTER TABLE requests ADD COLUMN IF NOT EXISTS request_params TEXT",
	} {
		if _, err := pool.Exec(context.Background(), stmt); err != nil {
			pool.Close()
			return nil, fmt.Errorf("migrate column: %w", err)
		}
	}
	if _, err := pool.Exec(context.Background(), "CREATE INDEX IF NOT EXISTS idx_requests_stop_reason ON requests(stop_reason)"); err != nil {
		pool.Close()
		return nil, fmt.Errorf("create stop_reason index: %w", err)
	}
	if _, err := pool.Exec(context.Background(), "CREATE INDEX IF NOT EXISTS idx_requests_error_type ON requests(error_type)"); err != nil {
		pool.Close()
		return nil, fmt.Errorf("create error_type index: %w", err)
	}
	if !compressor.Enabled {
		compressor = CompressionConfig{Enabled: false}
	}
	return &PostgresBackend{pool: pool, compressor: compressor}, nil
}

// SaveRequest inserts a new LLM request record into the database, including
// metadata, token usage, and the original request/response bodies.
func (p *PostgresBackend) SaveRequest(ctx context.Context, req *types.StoredRequest) error {
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
	reqBody, err := CompressBody([]byte(req.Request), p.compressor)
	if err != nil {
		return fmt.Errorf("compress request body: %w", err)
	}
	respBody, err := CompressBody([]byte(req.Response), p.compressor)
	if err != nil {
		return fmt.Errorf("compress response body: %w", err)
	}

	_, err = p.pool.Exec(ctx,
		`INSERT INTO requests (id, session_id, model, method, path, request_body, response_body,
		 input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens,
		 duration_ms, status_code, created_at,
		 system_prompt, stop_reason, error_type, error_message, ttft_ms, temperature, top_p, request_params)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14,
		 $15, $16, $17, $18, $19, $20, $21, $22)`,
		req.ID, req.SessionID, req.Model, req.Method, req.Path,
		string(reqBody), string(respBody),
		req.Usage.InputTokens, req.Usage.OutputTokens,
		req.Usage.CacheReadTokens, req.Usage.CacheCreationTokens,
		req.DurationMs, req.StatusCode,
		// CreatedAt is Unix milliseconds; convert to time.Time for the TIMESTAMP column.
		time.UnixMilli(req.CreatedAt),
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
func (p *PostgresBackend) GetSessionRequests(ctx context.Context, sessionID string, limit, offset int) ([]types.StoredRequest, error) {
	rows, err := p.pool.Query(ctx,
		`SELECT id, session_id, model, method, path, request_body, response_body,
		 input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens,
		 duration_ms, status_code, (EXTRACT(EPOCH FROM created_at) * 1000)::bigint,
		 system_prompt, stop_reason, error_type, error_message, ttft_ms, temperature, top_p, request_params
		 FROM requests WHERE session_id = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3`,
		sessionID, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("get session requests: %w", err)
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
			return nil, fmt.Errorf("scan session request: %w", err)
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
		return nil, fmt.Errorf("iterate session requests: %w", err)
	}
	return results, nil
}

// QueryRequests retrieves requests matching the given filter criteria (session,
// model, time range) with optional pagination. Results are ordered by creation
// time descending.
func (p *PostgresBackend) QueryRequests(ctx context.Context, filter types.RequestFilter) ([]types.StoredRequest, error) {
	query := `SELECT id, session_id, model, method, path, request_body, response_body,
		 input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens,
		 duration_ms, status_code, (EXTRACT(EPOCH FROM created_at) * 1000)::bigint,
		 system_prompt, stop_reason, error_type, error_message, ttft_ms, temperature, top_p, request_params
		 FROM requests`
	var conditions []string
	var args []any
	argIdx := 1

	if filter.SessionID != nil {
		conditions = append(conditions, fmt.Sprintf("session_id ILIKE $%d", argIdx))
		args = append(args, "%"+*filter.SessionID+"%")
		argIdx++
	}
	if filter.Model != nil {
		conditions = append(conditions, fmt.Sprintf("model ILIKE $%d", argIdx))
		args = append(args, "%"+*filter.Model+"%")
		argIdx++
	}
	if filter.From != nil {
		conditions = append(conditions, fmt.Sprintf("created_at >= $%d", argIdx))
		args = append(args, time.UnixMilli(*filter.From))
		argIdx++
	}
	if filter.To != nil {
		conditions = append(conditions, fmt.Sprintf("created_at <= $%d", argIdx))
		args = append(args, time.UnixMilli(*filter.To))
		argIdx++
	}
	if filter.StopReason != nil {
		conditions = append(conditions, fmt.Sprintf("stop_reason = $%d", argIdx))
		args = append(args, *filter.StopReason)
		argIdx++
	}
	if filter.ErrorType != nil {
		conditions = append(conditions, fmt.Sprintf("error_type = $%d", argIdx))
		args = append(args, *filter.ErrorType)
		argIdx++
	}
	if filter.MinDuration != nil {
		conditions = append(conditions, fmt.Sprintf("duration_ms >= $%d", argIdx))
		args = append(args, *filter.MinDuration)
		argIdx++
	}
	if filter.MaxDuration != nil {
		conditions = append(conditions, fmt.Sprintf("duration_ms <= $%d", argIdx))
		args = append(args, *filter.MaxDuration)
		argIdx++
	}
	if len(filter.StatusCodes) > 0 {
		placeholders := make([]string, len(filter.StatusCodes))
		for i, sc := range filter.StatusCodes {
			placeholders[i] = fmt.Sprintf("$%d", argIdx)
			args = append(args, sc)
			argIdx++
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
	if filter.Cursor != nil {
		cursorClause := ""
		if len(conditions) > 0 {
			cursorClause = fmt.Sprintf(" AND created_at < $%d", argIdx)
		} else {
			cursorClause = fmt.Sprintf(" WHERE created_at < $%d", argIdx)
		}
		query = query[:len(query)-len(" ORDER BY created_at DESC")] + cursorClause + " ORDER BY created_at DESC"
		args = append(args, filter.Cursor)
		argIdx++
	}
	if filter.Limit > 0 {
		query += fmt.Sprintf(" LIMIT $%d", argIdx)
		args = append(args, filter.Limit)
		argIdx++
	}
	if filter.Offset > 0 && filter.Cursor == nil {
		query += fmt.Sprintf(" OFFSET $%d", argIdx)
		args = append(args, filter.Offset)
		argIdx++
	}

	rows, err := p.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query requests: %w", err)
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
			return nil, fmt.Errorf("scan query result: %w", err)
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
		return nil, fmt.Errorf("iterate query results: %w", err)
	}
	return results, nil
}

// SaveAPIKey inserts or updates a managed API key record. The key hash is
// stored using bcrypt, and the prefix allows fast lookup during validation.
func (p *PostgresBackend) SaveAPIKey(ctx context.Context, key *APIKey) error {
	_, err := p.pool.Exec(ctx,
		`INSERT INTO api_keys (id, key_hash, key_prefix, name, enabled, created_at)
		 VALUES ($1, $2, $3, $4, $5, TO_TIMESTAMP($6))
		 ON CONFLICT(id) DO UPDATE SET enabled=EXCLUDED.enabled`,
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
func (p *PostgresBackend) GetAPIKeyByPrefix(ctx context.Context, prefix string) (*APIKey, error) {
	row := p.pool.QueryRow(ctx,
		`SELECT id, key_hash, key_prefix, name, enabled,
		 EXTRACT(EPOCH FROM created_at)::bigint FROM api_keys WHERE key_prefix = $1`, prefix,
	)
	var k APIKey
	err := row.Scan(&k.ID, &k.KeyHash, &k.KeyPrefix, &k.Name, &k.Enabled, &k.CreatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get api key by prefix: %w", err)
	}
	return &k, nil
}

// ListAPIKeys returns all stored API keys ordered by creation time descending.
func (p *PostgresBackend) ListAPIKeys(ctx context.Context) ([]APIKey, error) {
	rows, err := p.pool.Query(ctx,
		`SELECT id, key_hash, key_prefix, name, enabled,
		 EXTRACT(EPOCH FROM created_at)::bigint FROM api_keys ORDER BY created_at DESC`,
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
func (p *PostgresBackend) DisableAPIKey(ctx context.Context, id string) error {
	_, err := p.pool.Exec(ctx, `UPDATE api_keys SET enabled = false WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("disable api key: %w", err)
	}
	return nil
}

// SaveConfig upserts a runtime configuration entry. If the key already exists,
// its value, updated_at, and updated_by are replaced.
func (p *PostgresBackend) SaveConfig(ctx context.Context, entry *types.ConfigEntry) error {
	_, err := p.pool.Exec(ctx,
		`INSERT INTO runtime_config (key, value, updated_at, updated_by)
		 VALUES ($1, $2, TO_TIMESTAMP($3::double precision / 1000), $4)
		 ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value, updated_at=EXCLUDED.updated_at, updated_by=EXCLUDED.updated_by`,
		entry.Key, string(entry.Value), entry.UpdatedAt, entry.UpdatedBy,
	)
	if err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	return nil
}

// GetConfig retrieves a single runtime configuration entry by key. Returns
// nil without error if the key does not exist.
func (p *PostgresBackend) GetConfig(ctx context.Context, key string) (*types.ConfigEntry, error) {
	row := p.pool.QueryRow(ctx,
		`SELECT key, value, EXTRACT(EPOCH FROM updated_at)::bigint * 1000, updated_by
		 FROM runtime_config WHERE key = $1`, key,
	)
	var entry types.ConfigEntry
	var valueStr string
	err := row.Scan(&entry.Key, &valueStr, &entry.UpdatedAt, &entry.UpdatedBy)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get config: %w", err)
	}
	entry.Value = json.RawMessage(valueStr)
	return &entry, nil
}

// ListConfig returns all runtime configuration entries, ordered by key.
func (p *PostgresBackend) ListConfig(ctx context.Context) ([]types.ConfigEntry, error) {
	rows, err := p.pool.Query(ctx,
		`SELECT key, value, EXTRACT(EPOCH FROM updated_at)::bigint * 1000, updated_by
		 FROM runtime_config ORDER BY key`,
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
func (p *PostgresBackend) DeleteConfig(ctx context.Context, key string) error {
	_, err := p.pool.Exec(ctx, `DELETE FROM runtime_config WHERE key = $1`, key)
	if err != nil {
		return fmt.Errorf("delete config: %w", err)
	}
	return nil
}

// SaveAuditEntry inserts a new audit log entry and sets its ID from the
// auto-generated sequence.
func (p *PostgresBackend) SaveAuditEntry(ctx context.Context, entry *types.AuditEntry) error {
	err := p.pool.QueryRow(ctx,
		`INSERT INTO audit_log (action, target_key, old_value, new_value, performed_by, created_at)
		 VALUES ($1, $2, $3, $4, $5, TO_TIMESTAMP($6::double precision / 1000))
		 RETURNING id`,
		entry.Action, entry.TargetKey, entry.OldValue, entry.NewValue, entry.PerformedBy, entry.CreatedAt,
	).Scan(&entry.ID)
	if err != nil {
		return fmt.Errorf("save audit: %w", err)
	}
	return nil
}

// QueryAuditEntries returns audit log entries ordered by creation time
// descending, with pagination via limit and offset.
func (p *PostgresBackend) QueryAuditEntries(ctx context.Context, limit, offset int) ([]types.AuditEntry, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := p.pool.Query(ctx,
		`SELECT id, action, target_key, old_value, new_value, performed_by,
		 EXTRACT(EPOCH FROM created_at)::bigint * 1000
		 FROM audit_log ORDER BY created_at DESC LIMIT $1 OFFSET $2`, limit, offset,
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

// Close shuts down the PostgreSQL connection pool.
func (p *PostgresBackend) Close() error {
	p.pool.Close()
	return nil
}
