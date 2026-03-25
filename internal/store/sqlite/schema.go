package sqlite

const deadlineStateKey = "deadline"

var schemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS channel_subscriptions (
		channel_id INTEGER PRIMARY KEY,
		shown_assets INTEGER NOT NULL DEFAULT 0,
		locale TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS guild_configs (
		guild_id INTEGER PRIMARY KEY,
		channel_id INTEGER NULL,
		thread_id INTEGER NULL,
		shown_assets INTEGER NOT NULL DEFAULT 0,
		locale TEXT NOT NULL,
		mention_role_id INTEGER NULL,
		enabled INTEGER NOT NULL DEFAULT 1,
		include_images INTEGER NOT NULL DEFAULT 1
	)`,
	`CREATE TABLE IF NOT EXISTS user_profiles (
		user_id INTEGER PRIMARY KEY,
		shown_assets INTEGER NOT NULL DEFAULT 0,
		locale TEXT NOT NULL,
		subscribed INTEGER NOT NULL DEFAULT 1
	)`,
	`CREATE TABLE IF NOT EXISTS current_assets (
		position INTEGER PRIMARY KEY,
		name TEXT NULL,
		link TEXT NOT NULL UNIQUE,
		image TEXT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS bot_state (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL
	)`,
}
