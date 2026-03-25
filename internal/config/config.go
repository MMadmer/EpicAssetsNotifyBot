package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"epicassetsnotifybot/internal/model"
)

const (
	DefaultLocale  = "ru-RU"
	CommandPrefix  = "/assets "
	EnvBotToken    = "ASSETS_BOT_TOKEN"
	EnvBotLocale   = "ASSETS_BOT_LOCALE"
	EnvDataDir     = "ASSETS_BOT_DATA_DIR"
	EnvDatabaseURL = "ASSETS_BOT_DATABASE_URL"
	DefaultDBName  = "bot.db"
	DefaultLogName = "bot.log"
	LegacyChannels = "subscribers_channels_backup.json"
	LegacyUsers    = "subscribers_users_backup.json"
	LegacyAssets   = "assets_backup.json"
	LegacyDeadline = "deadline_backup.json"
)

// Settings contains the runtime configuration resolved from the environment.
type Settings struct {
	ProjectRoot   string
	Token         string
	CommandPrefix string
	Locale        string
	DataDir       string
	DatabaseURL   string
	LocalesDir    string
	LogPath       string
}

// Load resolves all runtime settings relative to projectRoot.
func Load(projectRoot string) Settings {
	root := cleanProjectRoot(projectRoot)
	dataDir := dataDir()
	return Settings{
		ProjectRoot:   root,
		Token:         strings.TrimSpace(os.Getenv(EnvBotToken)),
		CommandPrefix: CommandPrefix,
		Locale:        envOrDefault(EnvBotLocale, DefaultLocale),
		DataDir:       dataDir,
		DatabaseURL:   databaseURL(dataDir),
		LocalesDir:    filepath.Join(root, "locales"),
		LogPath:       filepath.Join(root, DefaultLogName),
	}
}

// LoadLegacySnapshot reads the legacy JSON backups if they exist and normalizes
// them into the current runtime model.
func LoadLegacySnapshot(dataDir string, normalizer *model.Normalizer) (model.LegacySnapshot, error) {
	if normalizer == nil {
		return model.LegacySnapshot{}, fmt.Errorf("normalizer is required")
	}

	paths := []string{
		filepath.Join(dataDir, LegacyChannels),
		filepath.Join(dataDir, LegacyUsers),
		filepath.Join(dataDir, LegacyAssets),
		filepath.Join(dataDir, LegacyDeadline),
	}

	hasAny := false
	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			hasAny = true
			break
		}
	}
	if !hasAny {
		return model.LegacySnapshot{}, nil
	}

	channels := readJSONFile(paths[0], []any{})
	users := readJSONFile(paths[1], []any{})
	assets := readJSONFile(paths[2], []any{})
	deadline := readJSONFile(paths[3], "")

	return model.LegacySnapshot{
		Channels:     normalizer.NormalizeChannels(channels),
		UserProfiles: normalizer.NormalizeUserProfiles(users),
		Assets:       normalizer.NormalizeAssets(assets),
		Deadline:     normalizer.NormalizeDeadline(deadline),
	}, nil
}

func cleanProjectRoot(projectRoot string) string {
	root := strings.TrimSpace(projectRoot)
	if root == "" {
		root = "."
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return filepath.Clean(root)
	}
	return filepath.Clean(abs)
}

func dataDir() string {
	if raw := strings.TrimSpace(os.Getenv(EnvDataDir)); raw != "" {
		return filepath.Clean(raw)
	}
	if runtime.GOOS == "windows" {
		return "data"
	}
	return "/data"
}

func databaseURL(dataDir string) string {
	if raw := strings.TrimSpace(os.Getenv(EnvDatabaseURL)); raw != "" {
		return raw
	}
	return sqliteURL(filepath.Join(dataDir, DefaultDBName))
}

func sqliteURL(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	return "sqlite+aiosqlite:///" + filepath.ToSlash(abs)
}

func envOrDefault(key, defaultValue string) string {
	if raw := strings.TrimSpace(os.Getenv(key)); raw != "" {
		return raw
	}
	return defaultValue
}

func readJSONFile(path string, fallback any) any {
	f, err := os.Open(path)
	if err != nil {
		return fallback
	}
	defer f.Close()

	payload, err := decodeJSON(f)
	if err != nil {
		return fallback
	}
	return payload
}

func decodeJSON(r io.Reader) (any, error) {
	body, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	body = bytes.TrimPrefix(body, []byte{0xEF, 0xBB, 0xBF})

	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	return payload, nil
}
