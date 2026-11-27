// Package store provides optional persistence for per-user proxy lists.
//
// Heroku's filesystem is ephemeral, so anything saved to disk disappears on
// restart. When DATABASE_URL is configured the bot persists user proxies in
// Postgres; without it, lists live only for the life of the process.
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Postgres implements proxy.Store.
type Postgres struct {
	pool *pgxpool.Pool
}

// OpenPostgres connects, verifies reachability and applies the schema.
func OpenPostgres(ctx context.Context, dsn string) (*Postgres, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	cfg.MaxConns = 4
	cfg.MaxConnIdleTime = 5 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}

	p := &Postgres{pool: pool}
	if err := p.migrate(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return p, nil
}

func (p *Postgres) migrate(ctx context.Context) error {
	_, err := p.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS user_proxies (
			user_id  BIGINT      NOT NULL,
			proxy    TEXT        NOT NULL,
			added_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY (user_id, proxy)
		)`)
	return err
}

// Load returns the user's saved proxies in insertion order.
func (p *Postgres) Load(ctx context.Context, userID int64) ([]string, error) {
	rows, err := p.pool.Query(ctx,
		`SELECT proxy FROM user_proxies WHERE user_id = $1 ORDER BY added_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Save replaces the user's list atomically.
func (p *Postgres) Save(ctx context.Context, userID int64, proxies []string) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `DELETE FROM user_proxies WHERE user_id = $1`, userID); err != nil {
		return err
	}

	for _, pr := range proxies {
		if _, err := tx.Exec(ctx,
			`INSERT INTO user_proxies (user_id, proxy) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
			userID, pr); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// Close releases the connection pool.
func (p *Postgres) Close() {
	if p != nil && p.pool != nil {
		p.pool.Close()
	}
}
