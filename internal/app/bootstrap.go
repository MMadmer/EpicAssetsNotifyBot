package app

import (
    "context"
    "encoding/json"
    "fmt"
    "io"
    "log"
    "os"
    "path/filepath"
    "strings"

    "epicassetsnotifybot/internal/config"
    botdiscord "epicassetsnotifybot/internal/discord"
    "epicassetsnotifybot/internal/fab"
    "epicassetsnotifybot/internal/i18n"
    "epicassetsnotifybot/internal/model"
    sqlitestore "epicassetsnotifybot/internal/store/sqlite"
)

type Application struct {
    runtime *botdiscord.Runtime
}

func New(projectRoot string) (*Application, error) {
    settings := config.Load(projectRoot)
    if err := configureLogging(settings.LogPath); err != nil {
        return nil, err
    }

    localizer, err := i18n.New(settings.Locale, config.DefaultLocale, settings.LocalesDir)
    if err != nil {
        return nil, fmt.Errorf("create localizer: %w", err)
    }

    store, err := sqlitestore.New(settings.DatabaseURL)
    if err != nil {
        return nil, fmt.Errorf("open sqlite store: %w", err)
    }

    browserSource, err := fab.NewBrowserSource()
    if err != nil {
        _ = store.Close()
        return nil, fmt.Errorf("create fab browser source: %w", err)
    }

    scraper := fab.NewScraper(browserSource)
    runtime, err := botdiscord.New(settings, localizer, &sqliteAdapter{inner: store}, scraper)
    if err != nil {
        _ = store.Close()
        return nil, err
    }

    return &Application{runtime: runtime}, nil
}

func (a *Application) Run(ctx context.Context) error {
    if a == nil || a.runtime == nil {
        return fmt.Errorf("application is not initialized")
    }
    return a.runtime.Run(ctx)
}

func configureLogging(logPath string) error {
    if logPath == "" {
        return nil
    }
    if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
        return fmt.Errorf("create log directory: %w", err)
    }
    file, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
    if err != nil {
        return fmt.Errorf("open log file: %w", err)
    }

    log.SetFlags(log.LstdFlags | log.Lmsgprefix)
    log.SetOutput(io.MultiWriter(os.Stdout, file))
    return nil
}

type sqliteAdapter struct {
    inner *sqlitestore.Store
}

func (a *sqliteAdapter) Initialize(ctx context.Context) error {
    return a.inner.Initialize(ctx)
}

func (a *sqliteAdapter) Close() error {
    return a.inner.Close()
}

func (a *sqliteAdapter) LoadSnapshot(ctx context.Context) (model.Snapshot, error) {
    snapshot, err := a.inner.LoadSnapshot(ctx)
    if err != nil {
        return model.Snapshot{}, err
    }
    return model.Snapshot{
        GuildConfigs: guildConfigsFromSQLite(snapshot.GuildConfigs),
        UserProfiles: userProfilesFromSQLite(snapshot.UserProfiles),
        Assets:       assetsFromSQLite(snapshot.Assets),
        Deadline:     deadlineFromSQLite(snapshot.Deadline),
    }, nil
}

func (a *sqliteAdapter) LoadLegacyChannelSubscriptions(ctx context.Context) ([]model.ChannelSubscription, error) {
    channels, err := a.inner.LoadLegacyChannelSubscriptions(ctx)
    if err != nil {
        return nil, err
    }
    result := make([]model.ChannelSubscription, 0, len(channels))
    for _, channel := range channels {
        result = append(result, model.ChannelSubscription{
            ID:          channel.ID,
            ShownAssets: channel.ShownAssets,
            Locale:      channel.Locale,
        })
    }
    return result, nil
}

func (a *sqliteAdapter) SaveSnapshot(ctx context.Context, snapshot model.Snapshot) error {
    return a.inner.SaveSnapshot(ctx, sqlitestore.Snapshot{
        GuildConfigs: guildConfigsToSQLite(snapshot.GuildConfigs),
        UserProfiles: userProfilesToSQLite(snapshot.UserProfiles),
        Assets:       assetsToSQLite(snapshot.Assets),
        Deadline:     deadlineToSQLite(snapshot.Deadline),
    })
}

func guildConfigsFromSQLite(items []sqlitestore.GuildConfig) []model.GuildConfig {
    result := make([]model.GuildConfig, 0, len(items))
    for _, item := range items {
        result = append(result, model.GuildConfig{
            GuildID:       item.GuildID,
            ChannelID:     cloneInt64Ptr(item.ChannelID),
            ThreadID:      cloneInt64Ptr(item.ThreadID),
            ShownAssets:   item.ShownAssets,
            Locale:        item.Locale,
            MentionRoleID: cloneInt64Ptr(item.MentionRoleID),
            Enabled:       item.Enabled,
            IncludeImages: item.IncludeImages,
        })
    }
    return result
}

func userProfilesFromSQLite(items []sqlitestore.UserProfile) []model.UserProfile {
    result := make([]model.UserProfile, 0, len(items))
    for _, item := range items {
        result = append(result, model.UserProfile{
            ID:          item.ID,
            ShownAssets: item.ShownAssets,
            Locale:      item.Locale,
            Subscribed:  item.Subscribed,
        })
    }
    return result
}

func assetsFromSQLite(items []sqlitestore.Asset) []model.Asset {
    result := make([]model.Asset, 0, len(items))
    for _, item := range items {
        result = append(result, model.Asset{
            Name:  cloneStringPtr(item.Name),
            Link:  item.Link,
            Image: cloneStringPtr(item.Image),
        })
    }
    return result
}

func deadlineFromSQLite(value any) model.StoredDeadline {
    switch deadline := value.(type) {
    case nil:
        return model.StoredDeadline{}
    case string:
        if deadline == "" {
            return model.StoredDeadline{}
        }
        raw := deadline
        return model.StoredDeadline{Raw: &raw}
    case map[string]any:
        info, ok := mapDeadlineInfo(deadline)
        if !ok {
            return model.StoredDeadline{}
        }
        return model.StoredDeadline{Structured: &info}
    case sqlitestore.DeadlineInfo:
        return model.StoredDeadline{Structured: &model.DeadlineInfo{
            Day:       deadline.Day,
            Month:     deadline.Month,
            Year:      deadline.Year,
            Hour:      deadline.Hour,
            Minute:    deadline.Minute,
            GMTOffset: deadline.GMTOffset,
        }}
    default:
        return model.StoredDeadline{}
    }
}

func guildConfigsToSQLite(items []model.GuildConfig) []sqlitestore.GuildConfig {
    result := make([]sqlitestore.GuildConfig, 0, len(items))
    for _, item := range items {
        result = append(result, sqlitestore.GuildConfig{
            GuildID:       item.GuildID,
            ChannelID:     cloneInt64Ptr(item.ChannelID),
            ThreadID:      cloneInt64Ptr(item.ThreadID),
            ShownAssets:   item.ShownAssets,
            Locale:        item.Locale,
            MentionRoleID: cloneInt64Ptr(item.MentionRoleID),
            Enabled:       item.Enabled,
            IncludeImages: item.IncludeImages,
        })
    }
    return result
}

func userProfilesToSQLite(items []model.UserProfile) []sqlitestore.UserProfile {
    result := make([]sqlitestore.UserProfile, 0, len(items))
    for _, item := range items {
        result = append(result, sqlitestore.UserProfile{
            ID:          item.ID,
            ShownAssets: item.ShownAssets,
            Locale:      item.Locale,
            Subscribed:  item.Subscribed,
        })
    }
    return result
}

func assetsToSQLite(items []model.Asset) []sqlitestore.Asset {
    result := make([]sqlitestore.Asset, 0, len(items))
    for _, item := range items {
        result = append(result, sqlitestore.Asset{
            Name:  cloneStringPtr(item.Name),
            Link:  item.Link,
            Image: cloneStringPtr(item.Image),
        })
    }
    return result
}

func deadlineToSQLite(deadline model.StoredDeadline) any {
    switch {
    case deadline.Structured != nil:
        return sqlitestore.DeadlineInfo{
            Day:       deadline.Structured.Day,
            Month:     deadline.Structured.Month,
            Year:      deadline.Structured.Year,
            Hour:      deadline.Structured.Hour,
            Minute:    deadline.Structured.Minute,
            GMTOffset: deadline.Structured.GMTOffset,
        }
    case deadline.Raw != nil:
        return *deadline.Raw
    default:
        return nil
    }
}

func mapDeadlineInfo(value map[string]any) (model.DeadlineInfo, bool) {
    required := []string{"day", "month", "year", "hour", "minute"}
    info := model.DeadlineInfo{}
    for _, key := range required {
        raw, ok := value[key]
        if !ok {
            return model.DeadlineInfo{}, false
        }
        number, ok := asInt(raw)
        if !ok {
            return model.DeadlineInfo{}, false
        }
        switch key {
        case "day":
            info.Day = number
        case "month":
            info.Month = number
        case "year":
            info.Year = number
        case "hour":
            info.Hour = number
        case "minute":
            info.Minute = number
        }
    }

    gmtOffset, ok := value["gmt_offset"].(string)
    if !ok || strings.TrimSpace(gmtOffset) == "" {
        return model.DeadlineInfo{}, false
    }
    info.GMTOffset = gmtOffset
    return info, true
}

func asInt(value any) (int, bool) {
    switch typed := value.(type) {
    case int:
        return typed, true
    case int64:
        return int(typed), true
    case float64:
        return int(typed), typed == float64(int(typed))
    case json.Number:
        number, err := typed.Int64()
        return int(number), err == nil
    default:
        return 0, false
    }
}

func cloneInt64Ptr(value *int64) *int64 {
    if value == nil {
        return nil
    }
    cloned := *value
    return &cloned
}

func cloneStringPtr(value *string) *string {
    if value == nil {
        return nil
    }
    cloned := *value
    return &cloned
}


