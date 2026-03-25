package discord

import (
    "context"
    "fmt"
    "log"
    "strconv"
    "strings"
    "time"

    "github.com/bwmarrin/discordgo"
)

func (r *Runtime) onMessageCreate(_ *discordgo.Session, message *discordgo.MessageCreate) {
    if message == nil || message.Author == nil || message.Author.Bot {
        return
    }
    if !strings.HasPrefix(message.Content, r.settings.CommandPrefix) {
        return
    }

    commandLine := strings.TrimSpace(strings.TrimPrefix(message.Content, r.settings.CommandPrefix))
    if commandLine == "" {
        return
    }

    fields := strings.Fields(commandLine)
    if len(fields) == 0 {
        return
    }

    command := strings.ToLower(fields[0])
    args := fields[1:]

    if err := r.do(context.Background(), func() error {
        return r.handleCommand(context.Background(), message, command, args)
    }); err != nil && !isContextDone(err) {
        log.Printf("command %q failed: %v", command, err)
    }
}

func (r *Runtime) handleCommand(ctx context.Context, message *discordgo.MessageCreate, command string, args []string) error {
    switch command {
    case "sub":
        return r.handleSubscribe(ctx, message)
    case "unsub":
        return r.handleUnsubscribe(ctx, message)
    case "enable", "on":
        return r.handleEnable(ctx, message)
    case "disable", "off":
        return r.handleDisable(ctx, message)
    case "set-channel", "setchannel":
        return r.handleSetChannel(ctx, message)
    case "set-thread", "setthread":
        return r.handleSetThread(ctx, message)
    case "clear-thread", "clearthread":
        return r.handleClearThread(ctx, message)
    case "set-role", "setrole":
        return r.handleSetRole(ctx, message)
    case "clear-role", "clearrole":
        return r.handleClearRole(ctx, message)
    case "images":
        mode := ""
        if len(args) > 0 {
            mode = args[0]
        }
        return r.handleImages(ctx, message, mode)
    case "settings", "config":
        return r.handleSettings(message)
    case "test":
        return r.handleTest(message)
    case "time":
        return r.handleTime(message)
    case "lang", "locale", "l":
        locale := ""
        if len(args) > 0 {
            locale = args[0]
        }
        return r.handleLang(ctx, message, locale)
    default:
        return nil
    }
}

func (r *Runtime) handleSubscribe(ctx context.Context, message *discordgo.MessageCreate) error {
    localizer := r.localizerForMessage(message)
    if !r.isDM(message) && !r.isAdmin(message) {
        return r.respond(message.ChannelID, localizer.T("errors.permission_denied", nil))
    }

    if r.isDM(message) {
        userID, _ := strconv.ParseInt(message.Author.ID, 10, 64)
        profile := r.ensureUserProfile(userID)
        if profile.Subscribed {
            return r.respond(message.ChannelID, localizer.T("subscribe.dm.already", nil))
        }

        profile.Subscribed = true
        profile.ShownAssets = false
        if err := r.respond(message.ChannelID, localizer.T("subscribe.dm.success", nil)); err != nil {
            return err
        }
        log.Printf("User %s subscribed to asset updates.", message.Author.ID)

        if len(r.assets) > 0 && !profile.ShownAssets {
            attachments, _ := r.buildAttachments(r.assets)
            body := r.composeMessage(r.assets, r.localizerForLocale(profile.Locale))
            dmChannel, err := r.session.UserChannelCreate(message.Author.ID)
            if err == nil {
                if err := r.sendAssetMessage(dmChannel.ID, body, attachments); err == nil {
                    profile.ShownAssets = true
                }
            }
        }
        return r.backupData(ctx)
    }

    channel, err := r.resolveChannelObject(message.ChannelID)
    if err != nil {
        return err
    }
    channelID, threadID := currentTargetIDs(channel)
    guildID, _ := strconv.ParseInt(message.GuildID, 10, 64)
    cfg := r.ensureGuildConfig(guildID)
    if cfg.Enabled && equalInt64Ptr(cfg.ChannelID, channelID) && equalInt64Ptr(cfg.ThreadID, threadID) {
        return r.respond(message.ChannelID, localizer.T("subscribe.channel.already", nil))
    }

    r.setGuildTarget(cfg, channelID, threadID)
    cfg.Enabled = true
    if err := r.respond(message.ChannelID, localizer.T("subscribe.channel.success", map[string]any{
        "channel_name": r.formatTargetLabel(*cfg, localizer),
    })); err != nil {
        return err
    }
    log.Printf("Guild %s subscribed via %s.", message.GuildID, r.formatTargetLabel(*cfg, localizer))
    r.sendCurrentAssetsToGuild(ctx, cfg)
    return r.backupData(ctx)
}

func (r *Runtime) handleUnsubscribe(ctx context.Context, message *discordgo.MessageCreate) error {
    localizer := r.localizerForMessage(message)
    if !r.isDM(message) && !r.isAdmin(message) {
        return r.respond(message.ChannelID, localizer.T("errors.permission_denied", nil))
    }

    if r.isDM(message) {
        userID, _ := strconv.ParseInt(message.Author.ID, 10, 64)
        profile := r.findUserProfile(userID)
        if profile != nil && profile.Subscribed {
            profile.Subscribed = false
            profile.ShownAssets = false
            if err := r.respond(message.ChannelID, localizer.T("unsubscribe.success", nil)); err != nil {
                return err
            }
            log.Printf("User %s unsubscribed from asset updates.", message.Author.ID)
            return r.backupData(ctx)
        }
        return r.respond(message.ChannelID, localizer.T("unsubscribe.dm.not_subscribed", nil))
    }

    guildID, _ := strconv.ParseInt(message.GuildID, 10, 64)
    cfg := r.findGuildConfig(guildID)
    if cfg == nil || !cfg.Enabled {
        return r.respond(message.ChannelID, localizer.T("unsubscribe.channel.not_subscribed", nil))
    }

    cfg.Enabled = false
    cfg.ShownAssets = false
    if err := r.respond(message.ChannelID, localizer.T("unsubscribe.success", nil)); err != nil {
        return err
    }
    log.Printf("Guild %s disabled asset updates.", message.GuildID)
    return r.backupData(ctx)
}

func (r *Runtime) handleEnable(ctx context.Context, message *discordgo.MessageCreate) error {
    if r.isDM(message) {
        return r.handleSubscribe(ctx, message)
    }

    localizer := r.localizerForMessage(message)
    if !r.isAdmin(message) {
        return r.respond(message.ChannelID, localizer.T("errors.permission_denied", nil))
    }

    guildID, _ := strconv.ParseInt(message.GuildID, 10, 64)
    cfg := r.ensureGuildConfig(guildID)
    if cfg.ChannelID == nil {
        channel, err := r.resolveChannelObject(message.ChannelID)
        if err != nil {
            return err
        }
        channelID, threadID := currentTargetIDs(channel)
        r.setGuildTarget(cfg, channelID, threadID)
    }

    if cfg.Enabled {
        return r.respond(message.ChannelID, localizer.T("server.enable.already", map[string]any{
            "target": r.formatTargetLabel(*cfg, localizer),
        }))
    }

    cfg.Enabled = true
    if err := r.respond(message.ChannelID, localizer.T("server.enable.success", map[string]any{
        "target": r.formatTargetLabel(*cfg, localizer),
    })); err != nil {
        return err
    }
    r.sendCurrentAssetsToGuild(ctx, cfg)
    return r.backupData(ctx)
}

func (r *Runtime) handleDisable(ctx context.Context, message *discordgo.MessageCreate) error {
    if r.isDM(message) {
        return r.handleUnsubscribe(ctx, message)
    }

    localizer := r.localizerForMessage(message)
    if !r.isAdmin(message) {
        return r.respond(message.ChannelID, localizer.T("errors.permission_denied", nil))
    }

    guildID, _ := strconv.ParseInt(message.GuildID, 10, 64)
    cfg := r.findGuildConfig(guildID)
    if cfg == nil || !cfg.Enabled {
        return r.respond(message.ChannelID, localizer.T("server.disable.already", nil))
    }

    cfg.Enabled = false
    cfg.ShownAssets = false
    if err := r.respond(message.ChannelID, localizer.T("server.disable.success", nil)); err != nil {
        return err
    }
    return r.backupData(ctx)
}

func (r *Runtime) handleSetChannel(ctx context.Context, message *discordgo.MessageCreate) error {
    localizer := r.localizerForMessage(message)
    if r.isDM(message) {
        return r.respond(message.ChannelID, localizer.T("errors.guild_only", nil))
    }
    if !r.isAdmin(message) {
        return r.respond(message.ChannelID, localizer.T("errors.permission_denied", nil))
    }

    channel, err := r.resolveChannelObject(message.ChannelID)
    if err != nil {
        return err
    }
    channelID, _ := currentTargetIDs(channel)
    guildID, _ := strconv.ParseInt(message.GuildID, 10, 64)
    cfg := r.ensureGuildConfig(guildID)
    if !r.setGuildTarget(cfg, channelID, nil) {
        return r.respond(message.ChannelID, localizer.T("server.channel.already", map[string]any{
            "target": r.formatTargetLabel(*cfg, localizer),
        }))
    }

    if err := r.respond(message.ChannelID, localizer.T("server.channel.updated", map[string]any{
        "target": r.formatTargetLabel(*cfg, localizer),
    })); err != nil {
        return err
    }
    r.sendCurrentAssetsToGuild(ctx, cfg)
    return r.backupData(ctx)
}

func (r *Runtime) handleSetThread(ctx context.Context, message *discordgo.MessageCreate) error {
    localizer := r.localizerForMessage(message)
    if r.isDM(message) {
        return r.respond(message.ChannelID, localizer.T("errors.guild_only", nil))
    }
    if !r.isAdmin(message) {
        return r.respond(message.ChannelID, localizer.T("errors.permission_denied", nil))
    }

    channel, err := r.resolveChannelObject(message.ChannelID)
    if err != nil {
        return err
    }
    if channel == nil || !isThreadChannel(channel.Type) {
        return r.respond(message.ChannelID, localizer.T("server.thread.not_in_thread", nil))
    }

    channelID, threadID := currentTargetIDs(channel)
    guildID, _ := strconv.ParseInt(message.GuildID, 10, 64)
    cfg := r.ensureGuildConfig(guildID)
    if !r.setGuildTarget(cfg, channelID, threadID) {
        return r.respond(message.ChannelID, localizer.T("server.thread.already", map[string]any{
            "target": r.formatTargetLabel(*cfg, localizer),
        }))
    }

    if err := r.respond(message.ChannelID, localizer.T("server.thread.updated", map[string]any{
        "target": r.formatTargetLabel(*cfg, localizer),
    })); err != nil {
        return err
    }
    r.sendCurrentAssetsToGuild(ctx, cfg)
    return r.backupData(ctx)
}

func (r *Runtime) handleClearThread(ctx context.Context, message *discordgo.MessageCreate) error {
    localizer := r.localizerForMessage(message)
    if r.isDM(message) {
        return r.respond(message.ChannelID, localizer.T("errors.guild_only", nil))
    }
    if !r.isAdmin(message) {
        return r.respond(message.ChannelID, localizer.T("errors.permission_denied", nil))
    }

    guildID, _ := strconv.ParseInt(message.GuildID, 10, 64)
    cfg := r.findGuildConfig(guildID)
    if cfg == nil || cfg.ThreadID == nil {
        return r.respond(message.ChannelID, localizer.T("server.thread.already_cleared", nil))
    }

    r.setGuildTarget(cfg, cfg.ChannelID, nil)
    if err := r.respond(message.ChannelID, localizer.T("server.thread.cleared", map[string]any{
        "target": r.formatTargetLabel(*cfg, localizer),
    })); err != nil {
        return err
    }
    r.sendCurrentAssetsToGuild(ctx, cfg)
    return r.backupData(ctx)
}

func (r *Runtime) handleSetRole(ctx context.Context, message *discordgo.MessageCreate) error {
    localizer := r.localizerForMessage(message)
    if r.isDM(message) {
        return r.respond(message.ChannelID, localizer.T("errors.guild_only", nil))
    }
    if !r.isAdmin(message) {
        return r.respond(message.ChannelID, localizer.T("errors.permission_denied", nil))
    }

    roleID, ok := parseRoleID(message)
    if !ok {
        return r.respond(message.ChannelID, localizer.T("errors.invalid_arguments", nil))
    }

    guildID, _ := strconv.ParseInt(message.GuildID, 10, 64)
    cfg := r.ensureGuildConfig(guildID)
    if cfg.MentionRoleID != nil && *cfg.MentionRoleID == roleID {
        return r.respond(message.ChannelID, localizer.T("server.role.already", map[string]any{
            "role": fmt.Sprintf("<@&%d>", roleID),
        }))
    }

    cfg.MentionRoleID = &roleID
    if err := r.respond(message.ChannelID, localizer.T("server.role.updated", map[string]any{
        "role": fmt.Sprintf("<@&%d>", roleID),
    })); err != nil {
        return err
    }
    return r.backupData(ctx)
}

func (r *Runtime) handleClearRole(ctx context.Context, message *discordgo.MessageCreate) error {
    localizer := r.localizerForMessage(message)
    if r.isDM(message) {
        return r.respond(message.ChannelID, localizer.T("errors.guild_only", nil))
    }
    if !r.isAdmin(message) {
        return r.respond(message.ChannelID, localizer.T("errors.permission_denied", nil))
    }

    guildID, _ := strconv.ParseInt(message.GuildID, 10, 64)
    cfg := r.findGuildConfig(guildID)
    if cfg == nil || cfg.MentionRoleID == nil {
        return r.respond(message.ChannelID, localizer.T("server.role.already_cleared", nil))
    }

    cfg.MentionRoleID = nil
    if err := r.respond(message.ChannelID, localizer.T("server.role.cleared", nil)); err != nil {
        return err
    }
    return r.backupData(ctx)
}

func (r *Runtime) handleImages(ctx context.Context, message *discordgo.MessageCreate, mode string) error {
    localizer := r.localizerForMessage(message)
    if r.isDM(message) {
        return r.respond(message.ChannelID, localizer.T("errors.guild_only", nil))
    }
    if !r.isAdmin(message) {
        return r.respond(message.ChannelID, localizer.T("errors.permission_denied", nil))
    }

    guildID, _ := strconv.ParseInt(message.GuildID, 10, 64)
    cfg := r.ensureGuildConfig(guildID)
    if strings.TrimSpace(mode) == "" {
        return r.respond(message.ChannelID, localizer.T("server.images.current", map[string]any{
            "value": r.formatBoolLabel(cfg.IncludeImages, localizer),
        }))
    }

    modeMap := map[string]bool{
        "on": true, "true": true, "yes": true, "1": true,
        "off": false, "false": false, "no": false, "0": false,
    }
    includeImages, ok := modeMap[strings.ToLower(strings.TrimSpace(mode))]
    if !ok {
        return r.respond(message.ChannelID, localizer.T("server.images.invalid", nil))
    }
    if cfg.IncludeImages == includeImages {
        return r.respond(message.ChannelID, localizer.T("server.images.already", map[string]any{
            "value": r.formatBoolLabel(cfg.IncludeImages, localizer),
        }))
    }

    cfg.IncludeImages = includeImages
    if err := r.respond(message.ChannelID, localizer.T("server.images.updated", map[string]any{
        "value": r.formatBoolLabel(includeImages, localizer),
    })); err != nil {
        return err
    }
    return r.backupData(ctx)
}

func (r *Runtime) handleSettings(message *discordgo.MessageCreate) error {
    localizer := r.localizerForMessage(message)
    if r.isDM(message) {
        userID, _ := strconv.ParseInt(message.Author.ID, 10, 64)
        profile := r.findUserProfile(userID)
        if profile == nil {
            return r.respond(message.ChannelID, localizer.T("server.settings.dm_not_configured", nil))
        }
        status := r.formatBoolLabel(profile.Subscribed, localizer)
        return r.respond(message.ChannelID, strings.Join([]string{
            localizer.T("server.settings.dm_header", nil),
            localizer.T("server.settings.dm_status", map[string]any{"value": status}),
            localizer.T("server.settings.locale", map[string]any{
                "locale_name": r.localizer.LocaleName(profile.Locale),
                "locale_code": profile.Locale,
            }),
        }, "\n"))
    }

    guildID, _ := strconv.ParseInt(message.GuildID, 10, 64)
    return r.respond(message.ChannelID, r.settingsMessage(r.findGuildConfig(guildID), localizer))
}

func (r *Runtime) handleTest(message *discordgo.MessageCreate) error {
    localizer := r.localizerForMessage(message)
    if r.isDM(message) {
        return r.respond(message.ChannelID, localizer.T("errors.guild_only", nil))
    }
    if !r.isAdmin(message) {
        return r.respond(message.ChannelID, localizer.T("errors.permission_denied", nil))
    }

    guildID, _ := strconv.ParseInt(message.GuildID, 10, 64)
    cfg := r.findGuildConfig(guildID)
    if cfg == nil {
        return r.respond(message.ChannelID, localizer.T("server.test.not_configured", nil))
    }

    targetChannelID := r.targetChannelID(*cfg)
    if targetChannelID == "" {
        return r.respond(message.ChannelID, localizer.T("server.test.target_missing", nil))
    }

    missingPermissions := r.missingTargetPermissions(targetChannelID, cfg.IncludeImages)
    blocking := make([]string, 0)
    for _, permission := range missingPermissions {
        if permission != "attach_files" {
            blocking = append(blocking, permission)
        }
    }
    if len(blocking) > 0 {
        return r.respond(message.ChannelID, localizer.T("server.test.missing_permissions", map[string]any{
            "permissions": strings.Join(blocking, ", "),
        }))
    }

    guildName := message.GuildID
    if r.session.State != nil {
        if guild, err := r.session.State.Guild(message.GuildID); err == nil && guild != nil {
            guildName = guild.Name
        }
    }
    testBody := localizer.T("server.test.body", map[string]any{"guild_name": guildName})
    if err := r.sendAssetMessage(targetChannelID, r.composeDeliveryContent(*cfg, testBody), nil); err != nil {
        return r.respond(message.ChannelID, localizer.T("server.test.failed", map[string]any{"error": err.Error()}))
    }

    confirmation := localizer.T("server.test.sent", map[string]any{
        "target": r.formatTargetLabel(*cfg, localizer),
    })
    for _, permission := range missingPermissions {
        if permission == "attach_files" {
            confirmation = strings.Join([]string{
                confirmation,
                localizer.T("server.test.missing_permissions", map[string]any{"permissions": "attach_files"}),
            }, "\n")
            break
        }
    }
    return r.respond(message.ChannelID, confirmation)
}

func (r *Runtime) handleTime(message *discordgo.MessageCreate) error {
    localizer := r.localizerForMessage(message)
    deleteHint := localizer.T("time.delete_hint", map[string]any{
        "delete_after": int(r.deleteAfter.Seconds()),
    })

    if !r.nextCheck.IsZero() {
        remaining := time.Until(r.nextCheck)
        if remaining < 0 {
            remaining = 0
        }
        totalSeconds := int(remaining.Seconds())
        hours := totalSeconds / 3600
        minutes := (totalSeconds % 3600) / 60
        seconds := totalSeconds % 60
        return r.sendTemporaryMessage(message.ChannelID, strings.Join([]string{
            localizer.T("time.remaining", map[string]any{
                "hours": hours,
                "minutes": minutes,
                "seconds": seconds,
            }),
            deleteHint,
        }, "\n"))
    }

    return r.sendTemporaryMessage(message.ChannelID, strings.Join([]string{
        localizer.T("time.no_schedule", nil),
        deleteHint,
    }, "\n"))
}

func (r *Runtime) handleLang(ctx context.Context, message *discordgo.MessageCreate, locale string) error {
    usage := r.localeUsage()
    if r.isDM(message) {
        userID, _ := strconv.ParseInt(message.Author.ID, 10, 64)
        localizer := r.localizer.ForLocale(r.userLocale(userID))
        currentLocale := r.userLocale(userID)

        if strings.TrimSpace(locale) == "" {
            return r.respond(message.ChannelID, strings.Join([]string{
                localizer.T("locale.dm.current", map[string]any{
                    "locale_name": r.localizer.LocaleName(currentLocale),
                    "locale_code": currentLocale,
                }),
                localizer.T("locale.available", map[string]any{"locales": r.availableLocalesLabel()}),
                localizer.T("locale.usage", map[string]any{"command": usage}),
            }, "\n"))
        }

        resolved := r.localizer.NormalizeLocale(locale)
        if resolved == "" {
            return r.respond(message.ChannelID, strings.Join([]string{
                localizer.T("locale.invalid", map[string]any{"input_value": locale}),
                localizer.T("locale.available", map[string]any{"locales": r.availableLocalesLabel()}),
                localizer.T("locale.usage", map[string]any{"command": usage}),
            }, "\n"))
        }
        if currentLocale == resolved {
            return r.respond(message.ChannelID, localizer.T("locale.dm.already", map[string]any{
                "locale_name": r.localizer.LocaleName(resolved),
                "locale_code": resolved,
            }))
        }

        profile := r.ensureUserProfile(userID)
        profile.Locale = resolved
        if err := r.backupData(ctx); err != nil {
            return err
        }
        newLocalizer := r.localizer.ForLocale(resolved)
        return r.respond(message.ChannelID, newLocalizer.T("locale.dm.changed", map[string]any{
            "locale_name": r.localizer.LocaleName(resolved),
            "locale_code": resolved,
        }))
    }

    localizer := r.localizerForMessage(message)
    guildID, _ := strconv.ParseInt(message.GuildID, 10, 64)
    currentLocale := r.guildLocale(guildID)
    if strings.TrimSpace(locale) == "" {
        return r.respond(message.ChannelID, strings.Join([]string{
            localizer.T("locale.server.current", map[string]any{
                "locale_name": r.localizer.LocaleName(currentLocale),
                "locale_code": currentLocale,
            }),
            localizer.T("locale.available", map[string]any{"locales": r.availableLocalesLabel()}),
            localizer.T("locale.usage", map[string]any{"command": usage}),
        }, "\n"))
    }
    if !r.isAdmin(message) {
        return r.respond(message.ChannelID, localizer.T("errors.permission_denied", nil))
    }

    resolved := r.localizer.NormalizeLocale(locale)
    if resolved == "" {
        return r.respond(message.ChannelID, strings.Join([]string{
            localizer.T("locale.invalid", map[string]any{"input_value": locale}),
            localizer.T("locale.available", map[string]any{"locales": r.availableLocalesLabel()}),
            localizer.T("locale.usage", map[string]any{"command": usage}),
        }, "\n"))
    }
    if currentLocale == resolved {
        return r.respond(message.ChannelID, localizer.T("locale.server.already", map[string]any{
            "locale_name": r.localizer.LocaleName(resolved),
            "locale_code": resolved,
        }))
    }

    cfg := r.ensureGuildConfig(guildID)
    cfg.Locale = resolved
    if err := r.backupData(ctx); err != nil {
        return err
    }
    newLocalizer := r.localizer.ForLocale(resolved)
    return r.respond(message.ChannelID, newLocalizer.T("locale.server.changed", map[string]any{
        "locale_name": r.localizer.LocaleName(resolved),
        "locale_code": resolved,
    }))
}

func (r *Runtime) respond(channelID, content string) error {
    _, err := r.session.ChannelMessageSend(channelID, content)
    return err
}

func (r *Runtime) isDM(message *discordgo.MessageCreate) bool {
    return message.GuildID == ""
}

func (r *Runtime) isAdmin(message *discordgo.MessageCreate) bool {
    if message == nil || message.Author == nil || message.GuildID == "" {
        return false
    }

    if message.Member != nil && message.Member.Permissions&discordgo.PermissionAdministrator != 0 {
        return true
    }

    guild, err := r.resolveGuild(message.GuildID)
    if err == nil && guild != nil && guild.OwnerID == message.Author.ID {
        return true
    }

    member := message.Member
    if member == nil {
        member, _ = r.session.GuildMember(message.GuildID, message.Author.ID)
    }
    if guild == nil || member == nil {
        return false
    }

    return memberHasAdministrator(guild, member)
}

func (r *Runtime) resolveGuild(guildID string) (*discordgo.Guild, error) {
    if guildID == "" {
        return nil, nil
    }
    if r.session.State != nil {
        if guild, err := r.session.State.Guild(guildID); err == nil && guild != nil {
            return guild, nil
        }
    }
    return r.session.Guild(guildID)
}

func memberHasAdministrator(guild *discordgo.Guild, member *discordgo.Member) bool {
    if guild == nil || member == nil {
        return false
    }

    if member.Permissions&discordgo.PermissionAdministrator != 0 {
        return true
    }

    roleIDs := map[string]struct{}{
        guild.ID: {},
    }
    for _, roleID := range member.Roles {
        roleIDs[roleID] = struct{}{}
    }

    for _, role := range guild.Roles {
        if _, ok := roleIDs[role.ID]; !ok {
            continue
        }
        if role.Permissions&discordgo.PermissionAdministrator != 0 {
            return true
        }
    }
    return false
}

func parseRoleID(message *discordgo.MessageCreate) (int64, bool) {
    if message == nil {
        return 0, false
    }
    if len(message.MentionRoles) > 0 {
        roleID, err := strconv.ParseInt(message.MentionRoles[0], 10, 64)
        return roleID, err == nil
    }

    fields := strings.Fields(strings.TrimSpace(message.Content))
    for _, field := range fields {
        field = strings.TrimPrefix(field, "<@&")
        field = strings.TrimSuffix(field, ">")
        roleID, err := strconv.ParseInt(field, 10, 64)
        if err == nil {
            return roleID, true
        }
    }
    return 0, false
}

