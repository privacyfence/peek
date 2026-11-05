package db

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/privacyfence/peek/internal/models"
)

// SetUploadBackend upserts the backend of one page version. The version must
// exist. A zero CreatedByAccountID is stored as NULL.
func (s *Store) SetUploadBackend(ctx context.Context, b models.UploadBackend) error {
	var creatorArg any
	if b.CreatedByAccountID > 0 {
		creatorArg = b.CreatedByAccountID
	}
	_, err := s.ExecContext(ctx, `INSERT INTO upload_backends(upload_id,version,image,port,created_by_account_id,created_at) VALUES(?,?,?,?,?,?)
		ON CONFLICT(upload_id,version) DO UPDATE SET image=excluded.image, port=excluded.port`,
		b.UploadID, b.Version, b.Image, b.Port, creatorArg, time.Now().Unix())
	return err
}

// GetUploadBackend returns the backend of a page version, or sql.ErrNoRows.
func (s *Store) GetUploadBackend(ctx context.Context, uploadID int64, version int) (*models.UploadBackend, error) {
	rows, err := s.QueryContext(ctx, uploadBackendSelect+` WHERE upload_id=? AND version=?`, uploadID, version)
	if err != nil {
		return nil, err
	}
	list, err := scanUploadBackends(rows)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, sql.ErrNoRows
	}
	return &list[0], nil
}

// ListUploadBackends returns a page's backends ordered by version.
func (s *Store) ListUploadBackends(ctx context.Context, uploadID int64) ([]models.UploadBackend, error) {
	rows, err := s.QueryContext(ctx, uploadBackendSelect+` WHERE upload_id=? ORDER BY version`, uploadID)
	if err != nil {
		return nil, err
	}
	return scanUploadBackends(rows)
}

// DeleteUploadBackend removes a version's backend and returns sql.ErrNoRows if
// there was none.
func (s *Store) DeleteUploadBackend(ctx context.Context, uploadID int64, version int) error {
	res, err := s.ExecContext(ctx, `DELETE FROM upload_backends WHERE upload_id=? AND version=?`, uploadID, version)
	if err != nil {
		return err
	}
	return requireRowsAffected(res)
}

// SetUploadBackendEnv upserts one environment variable of a page's backend.
// The value is stored as given; callers encrypt it.
func (s *Store) SetUploadBackendEnv(ctx context.Context, uploadID int64, name, value string) error {
	_, err := s.ExecContext(ctx, `INSERT INTO upload_backend_env(upload_id,name,value,updated_at) VALUES(?,?,?,?)
		ON CONFLICT(upload_id,name) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`,
		uploadID, name, value, time.Now().Unix())
	return err
}

// DeleteUploadBackendEnv removes one variable and returns sql.ErrNoRows if it
// did not exist.
func (s *Store) DeleteUploadBackendEnv(ctx context.Context, uploadID int64, name string) error {
	res, err := s.ExecContext(ctx, `DELETE FROM upload_backend_env WHERE upload_id=? AND name=?`, uploadID, name)
	if err != nil {
		return err
	}
	return requireRowsAffected(res)
}

// ListUploadBackendEnv returns a page's backend variables ordered by name.
func (s *Store) ListUploadBackendEnv(ctx context.Context, uploadID int64) ([]models.BackendEnvVar, error) {
	rows, err := s.QueryContext(ctx, `SELECT upload_id,name,value,updated_at FROM upload_backend_env WHERE upload_id=? ORDER BY name`, uploadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.BackendEnvVar
	for rows.Next() {
		var v models.BackendEnvVar
		var updated int64
		if err := rows.Scan(&v.UploadID, &v.Name, &v.Value, &updated); err != nil {
			return nil, err
		}
		v.UpdatedAt = time.Unix(updated, 0)
		out = append(out, v)
	}
	return out, rows.Err()
}

const uploadBackendSelect = `SELECT upload_id,version,image,port,created_by_account_id,created_at FROM upload_backends`

// scanUploadBackends reads every backend row and closes rows.
func scanUploadBackends(rows *sql.Rows) ([]models.UploadBackend, error) {
	defer rows.Close()
	var out []models.UploadBackend
	for rows.Next() {
		var b models.UploadBackend
		var creator sql.NullInt64
		var created int64
		if err := rows.Scan(&b.UploadID, &b.Version, &b.Image, &b.Port, &creator, &created); err != nil {
			return nil, err
		}
		b.CreatedByAccountID = creator.Int64
		b.CreatedAt = time.Unix(created, 0)
		out = append(out, b)
	}
	return out, rows.Err()
}

// UploadBackendTokenCalls reports whether the page's owner allowed API-token calls to its backend. No row means false.
func (s *Store) UploadBackendTokenCalls(ctx context.Context, uploadID int64) (bool, error) {
	var v int
	err := s.QueryRowContext(ctx, `SELECT allow_token_calls FROM upload_backend_settings WHERE upload_id=?`, uploadID).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return v != 0, nil
}

// SetUploadBackendTokenCalls upserts the page's token-call flag.
func (s *Store) SetUploadBackendTokenCalls(ctx context.Context, uploadID int64, enabled bool) error {
	v := 0
	if enabled {
		v = 1
	}
	_, err := s.ExecContext(ctx, `INSERT INTO upload_backend_settings(upload_id,allow_token_calls,updated_at) VALUES(?,?,?)
		ON CONFLICT(upload_id) DO UPDATE SET allow_token_calls=excluded.allow_token_calls, updated_at=excluded.updated_at`,
		uploadID, v, time.Now().Unix())
	return err
}

// RecordBackendTokenCall adds one call by accountID to a page version's counter and stores its time and status.
// identified says whether this call sent the account's identity to the backend; once set it stays set.
func (s *Store) RecordBackendTokenCall(ctx context.Context, uploadID int64, version int, accountID int64, status int, identified bool) error {
	id := 0
	if identified {
		id = 1
	}
	_, err := s.ExecContext(ctx, `INSERT INTO backend_token_calls(upload_id,version,account_id,calls,last_call_at,last_status,identified) VALUES(?,?,?,1,?,?,?)
		ON CONFLICT(upload_id,version,account_id) DO UPDATE SET calls=calls+1, last_call_at=excluded.last_call_at, last_status=excluded.last_status, identified=MAX(identified, excluded.identified)`,
		uploadID, version, accountID, time.Now().Unix(), status, id)
	return err
}

// ListBackendTokenCalls returns a page's token-call counters, newest last call first, at most limit rows.
func (s *Store) ListBackendTokenCalls(ctx context.Context, uploadID int64, limit int) ([]models.BackendTokenCall, error) {
	rows, err := s.QueryContext(ctx, `SELECT c.upload_id,c.version,c.account_id,COALESCE(a.email,''),c.calls,c.last_call_at,c.last_status,c.identified
		FROM backend_token_calls c LEFT JOIN accounts a ON a.id=c.account_id
		WHERE c.upload_id=? ORDER BY c.last_call_at DESC, c.version DESC, c.account_id LIMIT ?`, uploadID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.BackendTokenCall
	for rows.Next() {
		var c models.BackendTokenCall
		var ts, ident int64
		if err := rows.Scan(&c.UploadID, &c.Version, &c.AccountID, &c.Email, &c.Calls, &ts, &c.LastStatus, &ident); err != nil {
			return nil, err
		}
		c.LastCallAt = time.Unix(ts, 0)
		c.Identified = ident != 0
		out = append(out, c)
	}
	return out, rows.Err()
}
