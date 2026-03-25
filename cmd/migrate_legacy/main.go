package main

import (
    "context"
    "log"
    "os"

    "epicassetsnotifybot/internal/config"
    "epicassetsnotifybot/internal/i18n"
    "epicassetsnotifybot/internal/model"
    sqlitestore "epicassetsnotifybot/internal/store/sqlite"
)

func main() {
    projectRoot, err := os.Getwd()
    if err != nil {
        log.Fatalf("resolve project root: %v", err)
    }

    settings := config.Load(projectRoot)
    if err := os.MkdirAll(settings.DataDir, 0o755); err != nil {
        log.Fatalf("create data folder: %v", err)
    }

    localizer, err := i18n.New(settings.Locale, config.DefaultLocale, settings.LocalesDir)
    if err != nil {
        log.Fatalf("create localizer: %v", err)
    }

    baseLocale := localizer.NormalizeLocale(localizer.Locale())
    if baseLocale == "" {
        baseLocale = localizer.DefaultLocale()
    }

    normalizer := model.NewNormalizer(baseLocale, localizer.AvailableLocales())
    legacySnapshot, err := config.LoadLegacySnapshot(settings.DataDir, &normalizer)
    if err != nil {
        log.Fatalf("load legacy snapshot: %v", err)
    }

    store, err := sqlitestore.New(settings.DatabaseURL)
    if err != nil {
        log.Fatalf("open sqlite store: %v", err)
    }
    defer store.Close()

    ctx := context.Background()
    if err := store.Initialize(ctx); err != nil {
        log.Fatalf("initialize store: %v", err)
    }

    migrated, err := store.ImportLegacySnapshotIfEmpty(ctx, sqliteLegacyFromModel(legacySnapshot))
    if err != nil {
        log.Fatalf("import legacy snapshot: %v", err)
    }
    if !migrated {
        log.Printf("Migration skipped. The database already contains data or no legacy JSON files were found.")
        return
    }

    channels, err := store.LoadLegacyChannelSubscriptions(ctx)
    if err != nil {
        log.Fatalf("load legacy channels after migration: %v", err)
    }
    snapshot, err := store.LoadSnapshot(ctx)
    if err != nil {
        log.Fatalf("load snapshot after migration: %v", err)
    }

    log.Printf(
        "Migration completed successfully: %d legacy channels, %d user profiles, %d assets.",
        len(channels),
        len(snapshot.UserProfiles),
        len(snapshot.Assets),
    )
}

func sqliteLegacyFromModel(snapshot model.LegacySnapshot) sqlitestore.LegacySnapshot {
    return sqlitestore.LegacySnapshot{
        Channels:     sqliteChannels(snapshot.Channels),
        UserProfiles: sqliteUsers(snapshot.UserProfiles),
        Assets:       sqliteAssets(snapshot.Assets),
        Deadline:     sqliteDeadline(snapshot.Deadline),
    }
}

func sqliteChannels(items []model.ChannelSubscription) []sqlitestore.ChannelSubscription {
    result := make([]sqlitestore.ChannelSubscription, 0, len(items))
    for _, item := range items {
        result = append(result, sqlitestore.ChannelSubscription{
            ID: item.ID, ShownAssets: item.ShownAssets, Locale: item.Locale,
        })
    }
    return result
}

func sqliteUsers(items []model.UserProfile) []sqlitestore.UserProfile {
    result := make([]sqlitestore.UserProfile, 0, len(items))
    for _, item := range items {
        result = append(result, sqlitestore.UserProfile{
            ID: item.ID, ShownAssets: item.ShownAssets, Locale: item.Locale, Subscribed: item.Subscribed,
        })
    }
    return result
}

func sqliteAssets(items []model.Asset) []sqlitestore.Asset {
    result := make([]sqlitestore.Asset, 0, len(items))
    for _, item := range items {
        result = append(result, sqlitestore.Asset{
            Name: cloneStringPtr(item.Name), Link: item.Link, Image: cloneStringPtr(item.Image),
        })
    }
    return result
}

func sqliteDeadline(deadline model.StoredDeadline) any {
    switch {
    case deadline.Structured != nil:
        return sqlitestore.DeadlineInfo{
            Day: deadline.Structured.Day,
            Month: deadline.Structured.Month,
            Year: deadline.Structured.Year,
            Hour: deadline.Structured.Hour,
            Minute: deadline.Structured.Minute,
            GMTOffset: deadline.Structured.GMTOffset,
        }
    case deadline.Raw != nil:
        return *deadline.Raw
    default:
        return nil
    }
}

func cloneStringPtr(value *string) *string {
    if value == nil {
        return nil
    }
    cloned := *value
    return &cloned
}
