package discord

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"epicassetsnotifybot/internal/config"
	"epicassetsnotifybot/internal/fab"
	"epicassetsnotifybot/internal/i18n"
	"epicassetsnotifybot/internal/model"

	"github.com/bwmarrin/discordgo"
)

type Store interface {
	Initialize(ctx context.Context) error
	Close() error
	LoadSnapshot(ctx context.Context) (model.Snapshot, error)
	LoadLegacyChannelSubscriptions(ctx context.Context) ([]model.ChannelSubscription, error)
	SaveSnapshot(ctx context.Context, snapshot model.Snapshot) error
}

type operation struct {
	run    func() error
	result chan error
}

type Attachment struct {
	Filename string
	Content  []byte
}

type Runtime struct {
	settings  config.Settings
	localizer *i18n.Localizer
	store     Store
	scraper   fab.Getter
	session   *discordgo.Session
	http      *http.Client
	ops       chan operation

	baseLocale string

	guildConfigs []model.GuildConfig
	userProfiles []model.UserProfile
	assets       []model.Asset
	deadline     model.StoredDeadline
	nextCheck    time.Time

	deleteAfter time.Duration
	backupDelay time.Duration
	messageDelay time.Duration
}

func New(settings config.Settings, localizer *i18n.Localizer, store Store, scraper fab.Getter) (*Runtime, error) {
	if localizer == nil {
		return nil, fmt.Errorf("localizer is required")
	}
	if store == nil {
		return nil, fmt.Errorf("store is required")
	}
	if scraper == nil {
		return nil, fmt.Errorf("scraper is required")
	}
	if strings.TrimSpace(settings.Token) == "" {
		return nil, fmt.Errorf("%s is required", config.EnvBotToken)
	}

	session, err := discordgo.New("Bot " + settings.Token)
	if err != nil {
		return nil, fmt.Errorf("create discord session: %w", err)
	}

	session.Identify.Intents = discordgo.IntentsGuildMessages |
		discordgo.IntentsDirectMessages |
		discordgo.IntentsGuilds |
		discordgo.IntentsMessageContent

	baseLocale := localizer.NormalizeLocale(localizer.Locale())
	if baseLocale == "" {
		baseLocale = localizer.DefaultLocale()
	}

	runtime := &Runtime{
		settings:     settings,
		localizer:    localizer,
		store:        store,
		scraper:      scraper,
		session:      session,
		http:         &http.Client{Timeout: 30 * time.Second},
		ops:          make(chan operation),
		baseLocale:   baseLocale,
		deleteAfter:  10 * time.Second,
		backupDelay:  15 * time.Minute,
		messageDelay: 500 * time.Millisecond,
	}

	session.AddHandler(runtime.onMessageCreate)
	return runtime, nil
}

func (r *Runtime) Run(ctx context.Context) error {
	if _, err := os.Stat(r.settings.DataDir); errors.Is(err, os.ErrNotExist) {
		log.Printf("Creating data folder at %s", r.settings.DataDir)
	}
	if err := ensureDir(r.settings.DataDir); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}

	if err := r.store.Initialize(ctx); err != nil {
		return fmt.Errorf("initialize store: %w", err)
	}

	snapshot, err := r.store.LoadSnapshot(ctx)
	if err != nil {
		return fmt.Errorf("load snapshot: %w", err)
	}
	r.guildConfigs = cloneGuildConfigs(snapshot.GuildConfigs)
	r.userProfiles = cloneUserProfiles(snapshot.UserProfiles)
	r.assets = cloneAssets(snapshot.Assets)
	r.deadline = snapshot.Deadline

	loopCtx, cancelLoop := context.WithCancel(ctx)
	defer cancelLoop()
	go r.stateLoop(loopCtx)

	if err := r.session.Open(); err != nil {
		_ = r.store.Close()
		return fmt.Errorf("open discord session: %w", err)
	}
	log.Printf("Logged in as %s", r.session.State.User.Username)

	if err := r.do(loopCtx, func() error {
		return r.migrateLegacyServerConfigs(loopCtx)
	}); err != nil {
		log.Printf("legacy server config migration failed: %v", err)
	}

	go r.runDailyCheck(loopCtx)
	go r.runBackupLoop(loopCtx)

	<-ctx.Done()
	cancelLoop()

	if err := r.session.Close(); err != nil {
		log.Printf("close discord session: %v", err)
	}
	if err := r.store.Close(); err != nil {
		return fmt.Errorf("close store: %w", err)
	}
	return nil
}

func (r *Runtime) stateLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case op := <-r.ops:
			op.result <- op.run()
			close(op.result)
		}
	}
}

func (r *Runtime) do(ctx context.Context, run func() error) error {
	op := operation{
		run:    run,
		result: make(chan error, 1),
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case r.ops <- op:
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-op.result:
		return err
	}
}

func (r *Runtime) runDailyCheck(ctx context.Context) {
	for {
		err := r.do(ctx, func() error {
			r.nextCheck = time.Now().Add(24 * time.Hour)
			return r.checkAndNotifyAssets(ctx)
		})
		if err != nil && !isContextDone(err) {
			log.Printf("scheduled asset check failed: %v", err)
		}
		if sleepErr := sleepContext(ctx, 24*time.Hour); sleepErr != nil {
			return
		}
	}
}

func (r *Runtime) runBackupLoop(ctx context.Context) {
	for {
		if sleepErr := sleepContext(ctx, r.backupDelay); sleepErr != nil {
			return
		}
		err := r.do(ctx, func() error {
			return r.backupData(ctx)
		})
		if err != nil && !isContextDone(err) {
			log.Printf("scheduled database sync failed: %v", err)
		}
	}
}

func (r *Runtime) backupData(ctx context.Context) error {
	snapshot := model.Snapshot{
		GuildConfigs: cloneGuildConfigs(r.guildConfigs),
		UserProfiles: cloneUserProfiles(r.userProfiles),
		Assets:       cloneAssets(r.assets),
		Deadline:     r.deadline,
	}
	if err := r.store.SaveSnapshot(ctx, snapshot); err != nil {
		return err
	}
	log.Printf("Persisted bot state to database.")
	return nil
}

func (r *Runtime) migrateLegacyServerConfigs(ctx context.Context) error {
	if len(r.guildConfigs) > 0 {
		return nil
	}

	legacyChannels, err := r.store.LoadLegacyChannelSubscriptions(ctx)
	if err != nil {
		return err
	}
	if len(legacyChannels) == 0 {
		return nil
	}

	converted := make(map[int64]model.GuildConfig)
	duplicateChannels := make(map[int64][]int64)
	var unresolved []int64

	for _, legacy := range legacyChannels {
		channel, err := r.resolveChannelObject(strconv.FormatInt(legacy.ID, 10))
		if err != nil || channel == nil || channel.GuildID == "" {
			unresolved = append(unresolved, legacy.ID)
			continue
		}

		guildID, err := strconv.ParseInt(channel.GuildID, 10, 64)
		if err != nil {
			unresolved = append(unresolved, legacy.ID)
			continue
		}

		if _, exists := converted[guildID]; exists {
			duplicateChannels[guildID] = append(duplicateChannels[guildID], legacy.ID)
			continue
		}

		channelID := legacy.ID
		converted[guildID] = model.GuildConfig{
			GuildID:       guildID,
			ChannelID:     &channelID,
			ThreadID:      nil,
			ShownAssets:   legacy.ShownAssets,
			Locale:        legacy.Locale,
			MentionRoleID: nil,
			Enabled:       true,
			IncludeImages: true,
		}
	}

	if len(converted) == 0 {
		return nil
	}

	r.guildConfigs = make([]model.GuildConfig, 0, len(converted))
	for _, cfg := range converted {
		r.guildConfigs = append(r.guildConfigs, cfg)
	}
	if err := r.backupData(ctx); err != nil {
		return err
	}

	log.Printf("Migrated %d legacy channel subscriptions into guild configs.", len(converted))
	if len(duplicateChannels) > 0 {
		log.Printf("Multiple legacy subscribed channels were found in some guilds. Only the first channel per guild was migrated: %v", duplicateChannels)
	}
	if len(unresolved) > 0 {
		log.Printf("Some legacy subscribed channels could not be resolved and were left untouched: %v", unresolved)
	}

	return nil
}

func (r *Runtime) checkAndNotifyAssets(ctx context.Context) error {
	assets, deadlineInfo, err := r.scraper.GetFreeAssets(ctx)
	if errors.Is(err, fab.ErrLimitedTimeFreeNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(assets) == 0 {
		return nil
	}

	newAssets := mapFabAssets(assets)
	newDeadline := mapFabDeadline(deadlineInfo)

	newIDs := assetIDSet(newAssets)
	oldIDs := assetIDSet(r.assets)
	deadlineChanged := r.deadlineSignature(newDeadline) != r.deadlineSignature(r.deadline)

	added := difference(newIDs, oldIDs)
	if !deadlineChanged && len(added) == 0 && subset(newIDs, oldIDs) && len(newIDs) != len(oldIDs) {
		log.Printf("Shrink-only change detected, likely transient scrape issue. Skipping update.")
		return nil
	}
	if !deadlineChanged && equalSets(newIDs, oldIDs) {
		return nil
	}

	r.assets = newAssets
	r.deadline = newDeadline

	attachmentsCache := []Attachment(nil)
	messageCache := make(map[string]string)

	currentAttachments := func() ([]Attachment, error) {
		if attachmentsCache != nil {
			return attachmentsCache, nil
		}
		built, err := r.buildAttachments(r.assets)
		if err != nil {
			return nil, err
		}
		attachmentsCache = built
		return attachmentsCache, nil
	}

	for _, cfg := range r.enabledGuildConfigs() {
		guildLocale := cfg.Locale
		message, ok := messageCache[guildLocale]
		if !ok {
			message = r.composeMessage(r.assets, r.localizerForLocale(guildLocale))
			messageCache[guildLocale] = message
		}

		channelID := r.targetChannelID(cfg)
		if channelID == "" {
			log.Printf("Configured target for guild %d could not be resolved.", cfg.GuildID)
			_ = sleepContext(ctx, r.messageDelay)
			continue
		}

		attachments := []Attachment{}
		if cfg.IncludeImages {
			cached, err := currentAttachments()
			if err != nil {
				log.Printf("attachment build failed: %v", err)
			} else {
				attachments = cached
			}
		}

		if err := r.sendAssetMessage(channelID, r.composeDeliveryContent(cfg, message), attachments); err != nil {
			log.Printf("Send failed for guild %d: %v", cfg.GuildID, err)
		} else {
			if current := r.findGuildConfig(cfg.GuildID); current != nil {
				current.ShownAssets = true
			}
		}
		_ = sleepContext(ctx, r.messageDelay)
	}

	for _, user := range r.subscribedUsers() {
		userLocale := user.Locale
		message, ok := messageCache[userLocale]
		if !ok {
			message = r.composeMessage(r.assets, r.localizerForLocale(userLocale))
			messageCache[userLocale] = message
		}

		attachments, err := currentAttachments()
		if err != nil {
			log.Printf("attachment build failed: %v", err)
		}

		channel, err := r.session.UserChannelCreate(strconv.FormatInt(user.ID, 10))
		if err != nil {
			log.Printf("DM channel create failed for %d: %v", user.ID, err)
			_ = sleepContext(ctx, r.messageDelay)
			continue
		}
		if err := r.sendAssetMessage(channel.ID, message, attachments); err != nil {
			log.Printf("DM failed for %d: %v", user.ID, err)
		} else if current := r.findUserProfile(user.ID); current != nil {
			current.ShownAssets = true
		}
		_ = sleepContext(ctx, r.messageDelay)
	}

	return r.backupData(ctx)
}

func (r *Runtime) buildAttachments(assets []model.Asset) ([]Attachment, error) {
	attachments := make([]Attachment, 0)
	for _, asset := range assets {
		if asset.Image == nil || strings.TrimSpace(*asset.Image) == "" {
			continue
		}

		req, err := http.NewRequest(http.MethodGet, *asset.Image, nil)
		if err != nil {
			log.Printf("Image request build failed for %s: %v", asset.Link, err)
			continue
		}
		resp, err := r.http.Do(req)
		if err != nil {
			log.Printf("Image fetch failed for %s: %v", asset.Link, err)
			continue
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			log.Printf("Image read failed for %s: %v", asset.Link, err)
			continue
		}

		filename := safeFilename(derefString(asset.Name, "image")) + ".png"
		attachments = append(attachments, Attachment{Filename: filename, Content: body})
	}
	return attachments, nil
}

func (r *Runtime) sendAssetMessage(channelID, content string, attachments []Attachment) error {
	msg := &discordgo.MessageSend{Content: content}
	if len(attachments) > 0 {
		files := make([]*discordgo.File, 0, len(attachments))
		for _, attachment := range attachments {
			files = append(files, &discordgo.File{
				Name:   attachment.Filename,
				Reader: bytes.NewReader(attachment.Content),
			})
		}
		msg.Files = files
	}
	_, err := r.session.ChannelMessageSendComplex(channelID, msg)
	return err
}

func (r *Runtime) sendTemporaryMessage(channelID, content string) error {
	message, err := r.session.ChannelMessageSend(channelID, content)
	if err != nil {
		return err
	}

	go func() {
		timer := time.NewTimer(r.deleteAfter)
		defer timer.Stop()
		<-timer.C
		_ = r.session.ChannelMessageDelete(channelID, message.ID)
	}()
	return nil
}

func (r *Runtime) resolveChannelObject(channelID string) (*discordgo.Channel, error) {
	if channelID == "" {
		return nil, nil
	}
	if r.session.State != nil {
		if channel, err := r.session.State.Channel(channelID); err == nil && channel != nil {
			return channel, nil
		}
	}
	return r.session.Channel(channelID)
}

func (r *Runtime) composeMessage(assets []model.Asset, localizer *i18n.Localizer) string {
	var builder strings.Builder
	builder.WriteString(r.composeHeader(localizer))
	for _, asset := range assets {
		if strings.TrimSpace(asset.Link) == "" {
			continue
		}
		builder.WriteString("- [")
		builder.WriteString(derefString(asset.Name, localizer.T("assets.untitled", nil)))
		builder.WriteString("](<")
		builder.WriteString(asset.Link)
		builder.WriteString(">)\n")
	}
	return builder.String()
}

func (r *Runtime) composeHeader(localizer *i18n.Localizer) string {
	monthName := localizer.MonthName(int(time.Now().Month()), "standalone")
	deadlineSuffix := r.formatDeadlineSuffix(localizer)
	if deadlineSuffix != "" {
		return localizer.T("header.with_deadline", map[string]any{
			"month_name":      monthName,
			"deadline_suffix": deadlineSuffix,
		})
	}
	return localizer.T("header.without_deadline", map[string]any{
		"month_name": monthName,
	})
}

func (r *Runtime) formatDeadlineSuffix(localizer *i18n.Localizer) string {
	switch {
	case r.deadline.Raw != nil:
		return *r.deadline.Raw
	case r.deadline.Structured != nil:
		return localizer.T("deadline.until", map[string]any{
			"day":        r.deadline.Structured.Day,
			"month_name": localizer.MonthName(r.deadline.Structured.Month, "format"),
			"hour":       r.deadline.Structured.Hour,
			"minute":     r.deadline.Structured.Minute,
			"gmt_offset": r.deadline.Structured.GMTOffset,
		})
	default:
		return ""
	}
}

func (r *Runtime) composeDeliveryContent(config model.GuildConfig, message string) string {
	if config.MentionRoleID == nil {
		return message
	}
	return fmt.Sprintf("<@&%d>\n%s", *config.MentionRoleID, message)
}

func (r *Runtime) localizerForLocale(locale string) *i18n.Localizer {
	return r.localizer.ForLocale(locale)
}

func (r *Runtime) guildLocale(guildID int64) string {
	if cfg := r.findGuildConfig(guildID); cfg != nil {
		return cfg.Locale
	}
	return r.baseLocale
}

func (r *Runtime) userLocale(userID int64) string {
	if profile := r.findUserProfile(userID); profile != nil {
		return profile.Locale
	}
	return r.baseLocale
}

func (r *Runtime) localizerForMessage(message *discordgo.MessageCreate) *i18n.Localizer {
	if message.GuildID == "" {
		userID, _ := strconv.ParseInt(message.Author.ID, 10, 64)
		return r.localizer.ForLocale(r.userLocale(userID))
	}
	guildID, _ := strconv.ParseInt(message.GuildID, 10, 64)
	return r.localizer.ForLocale(r.guildLocale(guildID))
}

func (r *Runtime) findGuildConfig(guildID int64) *model.GuildConfig {
	for idx := range r.guildConfigs {
		if r.guildConfigs[idx].GuildID == guildID {
			return &r.guildConfigs[idx]
		}
	}
	return nil
}

func (r *Runtime) ensureGuildConfig(guildID int64) *model.GuildConfig {
	if cfg := r.findGuildConfig(guildID); cfg != nil {
		return cfg
	}
	r.guildConfigs = append(r.guildConfigs, model.GuildConfig{
		GuildID:       guildID,
		Locale:        r.baseLocale,
		Enabled:       false,
		IncludeImages: true,
	})
	return &r.guildConfigs[len(r.guildConfigs)-1]
}

func (r *Runtime) setGuildTarget(cfg *model.GuildConfig, channelID, threadID *int64) bool {
	changed := !equalInt64Ptr(cfg.ChannelID, channelID) || !equalInt64Ptr(cfg.ThreadID, threadID)
	cfg.ChannelID = cloneInt64Ptr(channelID)
	cfg.ThreadID = cloneInt64Ptr(threadID)
	if changed {
		cfg.ShownAssets = false
	}
	return changed
}

func (r *Runtime) findUserProfile(userID int64) *model.UserProfile {
	for idx := range r.userProfiles {
		if r.userProfiles[idx].ID == userID {
			return &r.userProfiles[idx]
		}
	}
	return nil
}

func (r *Runtime) ensureUserProfile(userID int64) *model.UserProfile {
	if profile := r.findUserProfile(userID); profile != nil {
		return profile
	}
	r.userProfiles = append(r.userProfiles, model.UserProfile{
		ID:          userID,
		Locale:      r.baseLocale,
		ShownAssets: false,
		Subscribed:  false,
	})
	return &r.userProfiles[len(r.userProfiles)-1]
}

func (r *Runtime) targetChannelID(cfg model.GuildConfig) string {
	if cfg.ThreadID != nil {
		return strconv.FormatInt(*cfg.ThreadID, 10)
	}
	if cfg.ChannelID != nil {
		return strconv.FormatInt(*cfg.ChannelID, 10)
	}
	return ""
}

func (r *Runtime) enabledGuildConfigs() []model.GuildConfig {
	result := make([]model.GuildConfig, 0)
	for _, cfg := range r.guildConfigs {
		if cfg.Enabled && cfg.ChannelID != nil {
			result = append(result, cfg)
		}
	}
	return result
}

func (r *Runtime) subscribedUsers() []model.UserProfile {
	result := make([]model.UserProfile, 0)
	for _, profile := range r.userProfiles {
		if profile.Subscribed {
			result = append(result, profile)
		}
	}
	return result
}

func (r *Runtime) sendCurrentAssetsToGuild(ctx context.Context, cfg *model.GuildConfig) bool {
	if cfg == nil || !cfg.Enabled || len(r.assets) == 0 || cfg.ShownAssets {
		return false
	}

	channelID := r.targetChannelID(*cfg)
	if channelID == "" {
		log.Printf("Configured target for guild %d could not be resolved.", cfg.GuildID)
		return false
	}

	localizer := r.localizerForLocale(cfg.Locale)
	message := r.composeMessage(r.assets, localizer)

	attachments := []Attachment{}
	if cfg.IncludeImages {
		built, err := r.buildAttachments(r.assets)
		if err == nil {
			attachments = built
		}
	}

	if err := r.sendAssetMessage(channelID, r.composeDeliveryContent(*cfg, message), attachments); err != nil {
		log.Printf("Initial asset send failed for guild %d: %v", cfg.GuildID, err)
		return false
	}

	cfg.ShownAssets = true
	return true
}

func (r *Runtime) deadlineSignature(deadline model.StoredDeadline) string {
	switch {
	case deadline.Structured != nil:
		info := deadline.Structured
		return fmt.Sprintf("%d:%d:%d:%d:%d:%s", info.Year, info.Month, info.Day, info.Hour, info.Minute, info.GMTOffset)
	case deadline.Raw != nil:
		return *deadline.Raw
	default:
		return ""
	}
}

func (r *Runtime) nextCheckLabel(localizer *i18n.Localizer) string {
	if r.nextCheck.IsZero() {
		return localizer.T("server.settings.no_next_check", nil)
	}
	remaining := time.Until(r.nextCheck)
	if remaining < 0 {
		remaining = 0
	}
	totalSeconds := int(remaining.Seconds())
	hours := totalSeconds / 3600
	minutes := (totalSeconds % 3600) / 60
	seconds := totalSeconds % 60
	return localizer.T("time.remaining", map[string]any{
		"hours":   hours,
		"minutes": minutes,
		"seconds": seconds,
	})
}

func (r *Runtime) formatTargetLabel(cfg model.GuildConfig, localizer *i18n.Localizer) string {
	if cfg.ThreadID != nil {
		return localizer.T("server.target.thread", map[string]any{"thread_id": *cfg.ThreadID})
	}
	if cfg.ChannelID != nil {
		return localizer.T("server.target.channel", map[string]any{"channel_id": *cfg.ChannelID})
	}
	return localizer.T("server.target.none", nil)
}

func (r *Runtime) formatRoleLabel(cfg model.GuildConfig, localizer *i18n.Localizer) string {
	if cfg.MentionRoleID == nil {
		return localizer.T("server.target.none", nil)
	}
	return fmt.Sprintf("<@&%d>", *cfg.MentionRoleID)
}

func (r *Runtime) formatBoolLabel(value bool, localizer *i18n.Localizer) string {
	if value {
		return localizer.T("common.enabled", nil)
	}
	return localizer.T("common.disabled", nil)
}

func (r *Runtime) settingsMessage(cfg *model.GuildConfig, localizer *i18n.Localizer) string {
	if cfg == nil {
		return strings.Join([]string{
			localizer.T("server.settings.not_configured", nil),
			localizer.T("server.settings.hint", nil),
		}, "\n")
	}

	channelValue := localizer.T("server.target.none", nil)
	if cfg.ChannelID != nil {
		channelValue = localizer.T("server.target.channel", map[string]any{"channel_id": *cfg.ChannelID})
	}
	threadValue := localizer.T("server.target.none", nil)
	if cfg.ThreadID != nil {
		threadValue = localizer.T("server.target.thread", map[string]any{"thread_id": *cfg.ThreadID})
	}

	return strings.Join([]string{
		localizer.T("server.settings.header", nil),
		localizer.T("server.settings.enabled", map[string]any{"value": r.formatBoolLabel(cfg.Enabled, localizer)}),
		localizer.T("server.settings.channel", map[string]any{"value": channelValue}),
		localizer.T("server.settings.thread", map[string]any{"value": threadValue}),
		localizer.T("server.settings.role", map[string]any{"value": r.formatRoleLabel(*cfg, localizer)}),
		localizer.T("server.settings.locale", map[string]any{
			"locale_name": r.localizer.LocaleName(cfg.Locale),
			"locale_code": cfg.Locale,
		}),
		localizer.T("server.settings.images", map[string]any{
			"value": r.formatBoolLabel(cfg.IncludeImages, localizer),
		}),
		localizer.T("server.settings.next_check", map[string]any{
			"value": r.nextCheckLabel(localizer),
		}),
	}, "\n")
}

func (r *Runtime) availableLocalesLabel() string {
	locales := r.localizer.AvailableLocales()
	codes := make([]string, 0, len(locales))
	for code := range locales {
		codes = append(codes, code)
	}
	slices.Sort(codes)

	labels := make([]string, 0, len(codes))
	seenShort := make(map[string]struct{})
	for _, code := range codes {
		short := strings.ToLower(strings.SplitN(code, "-", 2)[0])
		name := locales[code]
		if _, ok := seenShort[short]; ok {
			labels = append(labels, fmt.Sprintf("%s (%s)", code, name))
			continue
		}
		seenShort[short] = struct{}{}
		labels = append(labels, fmt.Sprintf("%s (%s)", short, name))
	}
	return strings.Join(labels, ", ")
}

func (r *Runtime) localeChoices() []string {
	locales := r.localizer.AvailableLocales()
	codes := make([]string, 0, len(locales))
	for code := range locales {
		codes = append(codes, code)
	}
	slices.Sort(codes)

	choices := make([]string, 0, len(codes))
	seen := make(map[string]struct{})
	for _, code := range codes {
		short := strings.ToLower(strings.SplitN(code, "-", 2)[0])
		if _, ok := seen[short]; ok {
			continue
		}
		seen[short] = struct{}{}
		choices = append(choices, short)
	}
	return choices
}

func (r *Runtime) localeUsage() string {
	return fmt.Sprintf("%slang <%s>", r.settings.CommandPrefix, strings.Join(r.localeChoices(), "|"))
}

func (r *Runtime) missingTargetPermissions(channelID string, includeImages bool) []string {
	if r.session.State == nil || r.session.State.User == nil {
		return nil
	}

	perms, err := r.session.State.UserChannelPermissions(r.session.State.User.ID, channelID)
	if err != nil {
		return nil
	}

	channel, err := r.resolveChannelObject(channelID)
	if err != nil || channel == nil {
		return nil
	}

	missing := make([]string, 0)
	if isThreadChannel(channel.Type) {
		if perms&discordgo.PermissionSendMessagesInThreads == 0 {
			missing = append(missing, "send_messages_in_threads")
		}
	} else if perms&discordgo.PermissionSendMessages == 0 {
		missing = append(missing, "send_messages")
	}

	if includeImages && perms&discordgo.PermissionAttachFiles == 0 {
		missing = append(missing, "attach_files")
	}
	return missing
}

func mapFabAssets(items []fab.Asset) []model.Asset {
	result := make([]model.Asset, 0, len(items))
	for _, item := range items {
		asset := model.Asset{Link: item.Link}
		if strings.TrimSpace(item.Name) != "" {
			name := item.Name
			asset.Name = &name
		}
		if strings.TrimSpace(item.Image) != "" {
			image := item.Image
			asset.Image = &image
		}
		result = append(result, asset)
	}
	return result
}

func mapFabDeadline(info *fab.DeadlineInfo) model.StoredDeadline {
	if info == nil {
		return model.StoredDeadline{}
	}
	return model.StoredDeadline{
		Structured: &model.DeadlineInfo{
			Day:       info.Day,
			Month:     info.Month,
			Year:      info.Year,
			Hour:      info.Hour,
			Minute:    info.Minute,
			GMTOffset: info.GMTOffset,
		},
	}
}

func cloneAssets(input []model.Asset) []model.Asset {
	output := make([]model.Asset, 0, len(input))
	for _, asset := range input {
		cloned := model.Asset{Link: asset.Link}
		if asset.Name != nil {
			name := *asset.Name
			cloned.Name = &name
		}
		if asset.Image != nil {
			image := *asset.Image
			cloned.Image = &image
		}
		output = append(output, cloned)
	}
	return output
}

func cloneGuildConfigs(input []model.GuildConfig) []model.GuildConfig {
	output := make([]model.GuildConfig, 0, len(input))
	for _, cfg := range input {
		cloned := cfg
		cloned.ChannelID = cloneInt64Ptr(cfg.ChannelID)
		cloned.ThreadID = cloneInt64Ptr(cfg.ThreadID)
		cloned.MentionRoleID = cloneInt64Ptr(cfg.MentionRoleID)
		output = append(output, cloned)
	}
	return output
}

func cloneUserProfiles(input []model.UserProfile) []model.UserProfile {
	output := make([]model.UserProfile, len(input))
	copy(output, input)
	return output
}

func cloneInt64Ptr(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func equalInt64Ptr(left, right *int64) bool {
	switch {
	case left == nil && right == nil:
		return true
	case left == nil || right == nil:
		return false
	default:
		return *left == *right
	}
}

func assetIDSet(assets []model.Asset) map[string]struct{} {
	result := make(map[string]struct{}, len(assets))
	for _, asset := range assets {
		if strings.TrimSpace(asset.Link) == "" {
			continue
		}
		result[asset.Link] = struct{}{}
	}
	return result
}

func difference(left, right map[string]struct{}) []string {
	result := make([]string, 0)
	for key := range left {
		if _, ok := right[key]; ok {
			continue
		}
		result = append(result, key)
	}
	return result
}

func subset(left, right map[string]struct{}) bool {
	for key := range left {
		if _, ok := right[key]; !ok {
			return false
		}
	}
	return true
}

func equalSets(left, right map[string]struct{}) bool {
	return len(left) == len(right) && subset(left, right)
}

func safeFilename(name string) string {
	re := regexp.MustCompile(`[\\/*?:"<>|]+`)
	safe := re.ReplaceAllString(strings.TrimSpace(name), "_")
	safe = strings.TrimSpace(safe)
	if safe == "" {
		return "image"
	}
	if len(safe) > 100 {
		return safe[:100]
	}
	return safe
}

func ensureDir(path string) error {
	return os.MkdirAll(path, 0o755)
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func isContextDone(err error) bool {
	return err == context.Canceled || err == context.DeadlineExceeded
}

func derefString(value *string, fallback string) string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return fallback
	}
	return *value
}

func isThreadChannel(channelType discordgo.ChannelType) bool {
	return channelType == discordgo.ChannelTypeGuildPublicThread ||
		channelType == discordgo.ChannelTypeGuildPrivateThread ||
		channelType == discordgo.ChannelTypeGuildNewsThread
}

func currentTargetIDs(channel *discordgo.Channel) (*int64, *int64) {
	if channel == nil {
		return nil, nil
	}
	channelID, channelErr := strconv.ParseInt(channel.ID, 10, 64)
	if channelErr != nil {
		return nil, nil
	}
	if isThreadChannel(channel.Type) {
		parentID, err := strconv.ParseInt(channel.ParentID, 10, 64)
		if err != nil {
			return nil, nil
		}
		return &parentID, &channelID
	}
	return &channelID, nil
}




