package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"

	_ "modernc.org/sqlite"
)

// New creates a SQLite-backed store using the same runtime tables as the Python bot.
// The databaseURL accepts the Python-style sqlite+aiosqlite:///... form as well as file paths.
func New(databaseURL string) (*Store, error) {
	dsn, err := normalizeDatabaseURL(databaseURL)
	if err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(1)

	return &Store{db: db}, nil
}

// Open is kept as a compatibility alias for callers that prefer that name.
func Open(databaseURL string) (*Store, error) {
	return New(databaseURL)
}

type Store struct {
	db     *sql.DB
	saveMu sync.Mutex
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) Initialize(ctx context.Context) error {
	if err := s.applyPragmas(ctx); err != nil {
		return err
	}

	for _, stmt := range schemaStatements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}

	return nil
}

func (s *Store) LoadSnapshot(ctx context.Context) (Snapshot, error) {
	guildConfigs, err := s.loadGuildConfigs(ctx)
	if err != nil {
		return Snapshot{}, err
	}

	userProfiles, err := s.loadUserProfiles(ctx)
	if err != nil {
		return Snapshot{}, err
	}

	assets, err := s.loadAssets(ctx)
	if err != nil {
		return Snapshot{}, err
	}

	deadline, err := s.loadDeadline(ctx)
	if err != nil {
		return Snapshot{}, err
	}

	return Snapshot{
		GuildConfigs: guildConfigs,
		UserProfiles: userProfiles,
		Assets:       assets,
		Deadline:     deadline,
	}, nil
}

func (s *Store) LoadLegacyChannelSubscriptions(ctx context.Context) ([]ChannelSubscription, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT channel_id, shown_assets, locale
		 FROM channel_subscriptions
		 ORDER BY channel_id`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []ChannelSubscription
	for rows.Next() {
		var record ChannelSubscription
		var shownAssets int
		if err := rows.Scan(&record.ID, &shownAssets, &record.Locale); err != nil {
			return nil, err
		}
		record.ShownAssets = shownAssets != 0
		result = append(result, record)
	}

	return result, rows.Err()
}

func (s *Store) SaveSnapshot(ctx context.Context, snapshot Snapshot) error {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if err = replaceRuntimeSnapshot(ctx, tx, snapshot); err != nil {
		_ = tx.Rollback()
		return err
	}

	if err = tx.Commit(); err != nil {
		return err
	}

	return nil
}

func (s *Store) ImportLegacySnapshotIfEmpty(ctx context.Context, snapshot LegacySnapshot) (bool, error) {
	if !legacySnapshotHasContent(snapshot) {
		return false, nil
	}

	s.saveMu.Lock()
	defer s.saveMu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	hasAny, err := hasAnyState(ctx, tx)
	if err != nil {
		_ = tx.Rollback()
		return false, err
	}
	if hasAny {
		_ = tx.Rollback()
		return false, nil
	}

	if err = replaceLegacySnapshot(ctx, tx, snapshot); err != nil {
		_ = tx.Rollback()
		return false, err
	}

	if err = tx.Commit(); err != nil {
		return false, err
	}

	log.Printf("Imported legacy JSON state into database.")
	return true, nil
}

func (s *Store) applyPragmas(ctx context.Context) error {
	for _, stmt := range []string{
		`PRAGMA foreign_keys=ON`,
		`PRAGMA busy_timeout=5000`,
		`PRAGMA synchronous=NORMAL`,
		`PRAGMA temp_store=MEMORY`,
	} {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}

	if _, err := s.db.ExecContext(ctx, `PRAGMA journal_mode=WAL`); err != nil {
		log.Printf("sqlite: unable to enable WAL mode: %v", err)
	}

	return nil
}

func (s *Store) loadGuildConfigs(ctx context.Context) ([]GuildConfig, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT guild_id, channel_id, thread_id, shown_assets, locale, mention_role_id, enabled, include_images
		 FROM guild_configs
		 ORDER BY guild_id`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []GuildConfig
	for rows.Next() {
		var (
			record      GuildConfig
			channelID   sql.NullInt64
			threadID    sql.NullInt64
			roleID      sql.NullInt64
			shownAssets int
			enabled     int
			images      int
		)

		if err := rows.Scan(
			&record.GuildID,
			&channelID,
			&threadID,
			&shownAssets,
			&record.Locale,
			&roleID,
			&enabled,
			&images,
		); err != nil {
			return nil, err
		}

		record.ChannelID = nullInt64Ptr(channelID)
		record.ThreadID = nullInt64Ptr(threadID)
		record.MentionRoleID = nullInt64Ptr(roleID)
		record.ShownAssets = shownAssets != 0
		record.Enabled = enabled != 0
		record.IncludeImages = images != 0
		result = append(result, record)
	}

	return result, rows.Err()
}

func (s *Store) loadUserProfiles(ctx context.Context) ([]UserProfile, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT user_id, shown_assets, locale, subscribed
		 FROM user_profiles
		 ORDER BY user_id`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []UserProfile
	for rows.Next() {
		var (
			record      UserProfile
			shownAssets int
			subscribed  int
		)

		if err := rows.Scan(&record.ID, &shownAssets, &record.Locale, &subscribed); err != nil {
			return nil, err
		}

		record.ShownAssets = shownAssets != 0
		record.Subscribed = subscribed != 0
		result = append(result, record)
	}

	return result, rows.Err()
}

func (s *Store) loadAssets(ctx context.Context) ([]Asset, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT position, name, link, image
		 FROM current_assets
		 ORDER BY position`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []Asset
	for rows.Next() {
		var (
			position int64
			name     sql.NullString
			link     string
			image    sql.NullString
		)

		if err := rows.Scan(&position, &name, &link, &image); err != nil {
			return nil, err
		}

		_ = position
		record := Asset{Link: link}
		if name.Valid {
			value := name.String
			record.Name = &value
		}
		if image.Valid {
			value := image.String
			record.Image = &value
		}
		result = append(result, record)
	}

	return result, rows.Err()
}

func (s *Store) loadDeadline(ctx context.Context) (any, error) {
	var raw string
	err := s.db.QueryRowContext(
		ctx,
		`SELECT value FROM bot_state WHERE key = ?`,
		deadlineStateKey,
	).Scan(&raw)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

	var payload any
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		log.Printf("sqlite: failed to decode deadline payload: %v", err)
		return nil, nil
	}

	switch payload.(type) {
	case nil, string, map[string]any:
		return payload, nil
	default:
		log.Printf("sqlite: unexpected deadline payload type %T, resetting to empty state", payload)
		return nil, nil
	}
}

func replaceRuntimeSnapshot(ctx context.Context, tx *sql.Tx, snapshot Snapshot) error {
	for _, stmt := range []string{
		`DELETE FROM guild_configs`,
		`DELETE FROM user_profiles`,
		`DELETE FROM current_assets`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}

	if err := insertGuildConfigs(ctx, tx, snapshot.GuildConfigs); err != nil {
		return err
	}
	if err := insertUserProfiles(ctx, tx, snapshot.UserProfiles); err != nil {
		return err
	}
	if err := insertAssets(ctx, tx, snapshot.Assets); err != nil {
		return err
	}
	return storeDeadline(ctx, tx, snapshot.Deadline)
}

func replaceLegacySnapshot(ctx context.Context, tx *sql.Tx, snapshot LegacySnapshot) error {
	for _, stmt := range []string{
		`DELETE FROM channel_subscriptions`,
		`DELETE FROM guild_configs`,
		`DELETE FROM user_profiles`,
		`DELETE FROM current_assets`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}

	if err := insertChannelSubscriptions(ctx, tx, snapshot.Channels); err != nil {
		return err
	}
	if err := insertUserProfiles(ctx, tx, snapshot.UserProfiles); err != nil {
		return err
	}
	if err := insertAssets(ctx, tx, snapshot.Assets); err != nil {
		return err
	}
	return storeDeadline(ctx, tx, snapshot.Deadline)
}

func insertChannelSubscriptions(ctx context.Context, tx *sql.Tx, items []ChannelSubscription) error {
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO channel_subscriptions(channel_id, shown_assets, locale) VALUES(?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, item := range items {
		if _, err := stmt.ExecContext(ctx, item.ID, boolToInt(item.ShownAssets), item.Locale); err != nil {
			return err
		}
	}
	return nil
}

func insertGuildConfigs(ctx context.Context, tx *sql.Tx, items []GuildConfig) error {
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO guild_configs(guild_id, channel_id, thread_id, shown_assets, locale, mention_role_id, enabled, include_images) VALUES(?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, item := range items {
		if _, err := stmt.ExecContext(
			ctx,
			item.GuildID,
			item.ChannelID,
			item.ThreadID,
			boolToInt(item.ShownAssets),
			item.Locale,
			item.MentionRoleID,
			boolToInt(item.Enabled),
			boolToInt(item.IncludeImages),
		); err != nil {
			return err
		}
	}
	return nil
}

func insertUserProfiles(ctx context.Context, tx *sql.Tx, items []UserProfile) error {
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO user_profiles(user_id, shown_assets, locale, subscribed) VALUES(?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, item := range items {
		if _, err := stmt.ExecContext(ctx, item.ID, boolToInt(item.ShownAssets), item.Locale, boolToInt(item.Subscribed)); err != nil {
			return err
		}
	}
	return nil
}

func insertAssets(ctx context.Context, tx *sql.Tx, items []Asset) error {
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO current_assets(position, name, link, image) VALUES(?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for idx, item := range items {
		position := idx + 1
		if item.Link == "" {
			continue
		}
		if _, err := stmt.ExecContext(ctx, position, item.Name, item.Link, item.Image); err != nil {
			return err
		}
	}
	return nil
}

func storeDeadline(ctx context.Context, tx *sql.Tx, deadline any) error {
	encoded, err := json.Marshal(deadline)
	if err != nil {
		return err
	}

	row := tx.QueryRowContext(ctx, `SELECT value FROM bot_state WHERE key = ?`, deadlineStateKey)
	var existing string
	switch err := row.Scan(&existing); {
	case err == nil:
		_, err = tx.ExecContext(ctx, `UPDATE bot_state SET value = ? WHERE key = ?`, string(encoded), deadlineStateKey)
		return err
	case errors.Is(err, sql.ErrNoRows):
		_, err = tx.ExecContext(ctx, `INSERT INTO bot_state(key, value) VALUES(?, ?)`, deadlineStateKey, string(encoded))
		return err
	default:
		return err
	}
}

func hasAnyState(ctx context.Context, tx *sql.Tx) (bool, error) {
	for _, query := range []string{
		`SELECT 1 FROM channel_subscriptions LIMIT 1`,
		`SELECT 1 FROM guild_configs LIMIT 1`,
		`SELECT 1 FROM user_profiles LIMIT 1`,
		`SELECT 1 FROM current_assets LIMIT 1`,
	} {
		var one int
		if err := tx.QueryRowContext(ctx, query).Scan(&one); err == nil {
			return true, nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
	}

	var value string
	err := tx.QueryRowContext(ctx, `SELECT value FROM bot_state WHERE key = ?`, deadlineStateKey).Scan(&value)
	if err == nil {
		return value != "null", nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return false, err
}

func legacySnapshotHasContent(snapshot LegacySnapshot) bool {
	return len(snapshot.Channels) > 0 ||
		len(snapshot.UserProfiles) > 0 ||
		len(snapshot.Assets) > 0 ||
		snapshot.Deadline != nil
}

func nullInt64Ptr(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	value := v.Int64
	return &value
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func normalizeDatabaseURL(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", errors.New("database url is empty")
	}

	switch {
	case strings.HasPrefix(value, "sqlite+aiosqlite:///"):
		value = strings.TrimPrefix(value, "sqlite+aiosqlite:///")
	case strings.HasPrefix(value, "sqlite:///"):
		value = strings.TrimPrefix(value, "sqlite:///")
	case strings.HasPrefix(value, "file:"):
		value = strings.TrimPrefix(value, "file:")
	}

	if value == ":memory:" {
		return "file::memory:?cache=shared", nil
	}

	path := filepath.FromSlash(value)
	if !filepath.IsAbs(path) {
		abs, err := filepath.Abs(path)
		if err != nil {
			return "", fmt.Errorf("resolve database path: %w", err)
		}
		path = abs
	}

	return "file:" + filepath.ToSlash(path), nil
}
