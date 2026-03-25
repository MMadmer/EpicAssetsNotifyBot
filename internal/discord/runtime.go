package discord

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"epicassetsnotifybot/internal/config"
	"epicassetsnotifybot/internal/fab"
	"epicassetsnotifybot/internal/i18n"
	"epicassetsnotifybot/internal/model"

	"github.com/bwmarrin/discordgo"
)

var invalidFilenamePattern = regexp.MustCompile(`[\\/*?:"<>|]+`)

type Store interface {
	Initialize(ctx context.Context) error
	Close() error
	LoadSnapshot(ctx context.Context) (model.Snapshot, error)
	LoadLegacyChannelSubscriptions(ctx context.Context) ([]model.ChannelSubscription, error)
	SaveSnapshot(ctx context.Context, snapshot model.Snapshot) error
}

type Attachment struct {
	Filename string
	Content  []byte
}

type attachmentCache struct {
	signature   string
	attachments []Attachment
}

type deliveryTask struct {
	guildID     int64
	userID      int64
	channelID   string
	content     string
	attachments []Attachment
}

type imageTask struct {
	index int
	name  string
	url   string
}

type Runtime struct {
	settings  config.Settings
	localizer *i18n.Localizer
	store     Store
	scraper   fab.Getter
	session   *discordgo.Session
	http      *http.Client

	baseLocale string

	mu               sync.RWMutex
	guildConfigs     map[int64]*model.GuildConfig
	userProfiles     map[int64]*model.UserProfile
	assets           []model.Asset
	deadline         model.StoredDeadline
	nextCheck        time.Time
	stateVersion     uint64
	persistedVersion uint64
	attachmentCache  attachmentCache

	flushCh chan struct{}

	deleteAfter        time.Duration
	backupDelay        time.Duration
	persistDebounce    time.Duration
	deliveryWorkers    int
	imageFetchWorkers  int
	attachmentMaxBytes int64
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

	transport := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		MaxIdleConns:        128,
		MaxIdleConnsPerHost: 32,
		MaxConnsPerHost:     64,
		IdleConnTimeout:     90 * time.Second,
		ForceAttemptHTTP2:   true,
	}

	runtime := &Runtime{
		settings:           settings,
		localizer:          localizer,
		store:              store,
		scraper:            scraper,
		session:            session,
		http:               &http.Client{Timeout: 30 * time.Second, Transport: transport},
		baseLocale:         baseLocale,
		guildConfigs:       make(map[int64]*model.GuildConfig),
		userProfiles:       make(map[int64]*model.UserProfile),
		flushCh:            make(chan struct{}, 1),
		deleteAfter:        10 * time.Second,
		backupDelay:        15 * time.Minute,
		persistDebounce:    2 * time.Second,
		deliveryWorkers:    8,
		imageFetchWorkers:  4,
		attachmentMaxBytes: 8 << 20,
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
	r.loadSnapshot(snapshot)

	loopCtx, cancelLoop := context.WithCancel(ctx)
	defer cancelLoop()

	go r.runFlushLoop(loopCtx)

	if err := r.session.Open(); err != nil {
		_ = r.store.Close()
		return fmt.Errorf("open discord session: %w", err)
	}
	log.Printf("Logged in as %s", r.session.State.User.Username)

	if err := r.migrateLegacyServerConfigs(loopCtx); err != nil {
		log.Printf("legacy server config migration failed: %v", err)
	}

	go r.runDailyCheck(loopCtx)

	<-ctx.Done()
	cancelLoop()

	if err := r.session.Close(); err != nil {
		log.Printf("close discord session: %v", err)
	}

	flushCtx, cancelFlush := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelFlush()
	if err := r.flushDirty(flushCtx); err != nil {
		log.Printf("final state flush failed: %v", err)
	}

	if closer, ok := r.scraper.(io.Closer); ok {
		if err := closer.Close(); err != nil {
			log.Printf("close scraper: %v", err)
		}
	}

	if err := r.store.Close(); err != nil {
		return fmt.Errorf("close store: %w", err)
	}
	return nil
}

func (r *Runtime) loadSnapshot(snapshot model.Snapshot) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.guildConfigs = guildConfigMap(snapshot.GuildConfigs)
	r.userProfiles = userProfileMap(snapshot.UserProfiles)
	r.assets = cloneAssets(snapshot.Assets)
	r.deadline = snapshot.Deadline
	r.nextCheck = time.Time{}
	r.stateVersion = 0
	r.persistedVersion = 0
	r.attachmentCache = attachmentCache{}
}

func (r *Runtime) runFlushLoop(ctx context.Context) {
	ticker := time.NewTicker(r.backupDelay)
	defer ticker.Stop()

	var (
		timer  *time.Timer
		timerC <-chan time.Time
	)

	for {
		select {
		case <-ctx.Done():
			stopTimer(timer)
			return
		case <-r.flushCh:
			if r.persistDebounce <= 0 {
				if err := r.flushDirty(ctx); err != nil && !isContextDone(err) {
					log.Printf("scheduled database sync failed: %v", err)
				}
				continue
			}
			if timer == nil {
				timer = time.NewTimer(r.persistDebounce)
			} else {
				stopTimer(timer)
				timer.Reset(r.persistDebounce)
			}
			timerC = timer.C
		case <-ticker.C:
			if err := r.flushDirty(ctx); err != nil && !isContextDone(err) {
				log.Printf("scheduled database sync failed: %v", err)
			}
		case <-timerC:
			timer = nil
			timerC = nil
			if err := r.flushDirty(ctx); err != nil && !isContextDone(err) {
				log.Printf("scheduled database sync failed: %v", err)
			}
		}
	}
}

func (r *Runtime) requestFlush() {
	select {
	case r.flushCh <- struct{}{}:
	default:
	}
}

func (r *Runtime) flushDirty(ctx context.Context) error {
	snapshot, version, ok := r.snapshotForSave()
	if !ok {
		return nil
	}
	if err := r.store.SaveSnapshot(ctx, snapshot); err != nil {
		return err
	}

	r.mu.Lock()
	if version > r.persistedVersion {
		r.persistedVersion = version
	}
	r.mu.Unlock()

	log.Printf("Persisted bot state to database.")
	return nil
}

func (r *Runtime) snapshotForSave() (model.Snapshot, uint64, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.persistedVersion >= r.stateVersion {
		return model.Snapshot{}, 0, false
	}

	return model.Snapshot{
		GuildConfigs: cloneGuildConfigMap(r.guildConfigs),
		UserProfiles: cloneUserProfileMap(r.userProfiles),
		Assets:       cloneAssets(r.assets),
		Deadline:     r.deadline,
	}, r.stateVersion, true
}

func (r *Runtime) markStateDirtyLocked() {
	r.stateVersion++
}

func (r *Runtime) runDailyCheck(ctx context.Context) {
	for {
		r.mu.Lock()
		r.nextCheck = time.Now().Add(24 * time.Hour)
		r.mu.Unlock()

		if err := r.checkAndNotifyAssets(ctx); err != nil && !isContextDone(err) {
			log.Printf("scheduled asset check failed: %v", err)
		}
		if sleepErr := sleepContext(ctx, 24*time.Hour); sleepErr != nil {
			return
		}
	}
}

func (r *Runtime) migrateLegacyServerConfigs(ctx context.Context) error {
	r.mu.RLock()
	hasConfigs := len(r.guildConfigs) > 0
	r.mu.RUnlock()
	if hasConfigs {
		return nil
	}

	legacyChannels, err := r.store.LoadLegacyChannelSubscriptions(ctx)
	if err != nil {
		return err
	}
	if len(legacyChannels) == 0 {
		return nil
	}

	converted := make(map[int64]model.GuildConfig, len(legacyChannels))
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

	r.mu.Lock()
	if len(r.guildConfigs) == 0 {
		for _, cfg := range converted {
			cloned := cloneGuildConfig(cfg)
			r.guildConfigs[cfg.GuildID] = &cloned
		}
		r.markStateDirtyLocked()
	}
	r.mu.Unlock()

	if err := r.flushDirty(ctx); err != nil {
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

	r.mu.Lock()
	oldIDs := assetIDSet(r.assets)
	deadlineChanged := r.deadlineSignature(newDeadline) != r.deadlineSignature(r.deadline)

	added := difference(newIDs, oldIDs)
	if !deadlineChanged && len(added) == 0 && subset(newIDs, oldIDs) && len(newIDs) != len(oldIDs) {
		r.mu.Unlock()
		log.Printf("Shrink-only change detected, likely transient scrape issue. Skipping update.")
		return nil
	}
	if !deadlineChanged && equalSets(newIDs, oldIDs) {
		r.mu.Unlock()
		return nil
	}

	r.assets = cloneAssets(newAssets)
	r.deadline = newDeadline
	r.attachmentCache = attachmentCache{}
	guildTargets := r.enabledGuildConfigsLocked()
	userTargets := r.subscribedUsersLocked()
	r.markStateDirtyLocked()
	r.mu.Unlock()

	guildSuccess, userSuccess := r.notifyRecipients(ctx, newAssets, guildTargets, userTargets)
	r.markRecipientsShown(guildSuccess, userSuccess)

	return r.flushDirty(ctx)
}

func (r *Runtime) notifyRecipients(ctx context.Context, assets []model.Asset, guilds []model.GuildConfig, users []model.UserProfile) ([]int64, []int64) {
	if len(guilds) == 0 && len(users) == 0 {
		return nil, nil
	}

	messageCache := make(map[string]string, len(guilds)+len(users))
	messageForLocale := func(locale string) string {
		if message, ok := messageCache[locale]; ok {
			return message
		}
		message := r.composeMessage(assets, r.localizerForLocale(locale))
		messageCache[locale] = message
		return message
	}

	needAttachments := len(users) > 0
	if !needAttachments {
		for _, cfg := range guilds {
			if cfg.IncludeImages {
				needAttachments = true
				break
			}
		}
	}

	var attachments []Attachment
	if needAttachments {
		built, err := r.getAttachments(ctx, assets)
		if err != nil {
			log.Printf("attachment build failed: %v", err)
		} else {
			attachments = built
		}
	}

	tasks := make([]deliveryTask, 0, len(guilds)+len(users))
	for _, cfg := range guilds {
		channelID := r.targetChannelID(cfg)
		if channelID == "" {
			log.Printf("Configured target for guild %d could not be resolved.", cfg.GuildID)
			continue
		}

		task := deliveryTask{
			guildID:   cfg.GuildID,
			channelID: channelID,
			content:   r.composeDeliveryContent(cfg, messageForLocale(cfg.Locale)),
		}
		if cfg.IncludeImages {
			task.attachments = attachments
		}
		tasks = append(tasks, task)
	}

	for _, user := range users {
		tasks = append(tasks, deliveryTask{
			userID:      user.ID,
			content:     messageForLocale(user.Locale),
			attachments: attachments,
		})
	}

	return r.runDeliveryTasks(ctx, tasks)
}

func (r *Runtime) runDeliveryTasks(ctx context.Context, tasks []deliveryTask) ([]int64, []int64) {
	if len(tasks) == 0 {
		return nil, nil
	}

	workerCount := r.deliveryWorkers
	if workerCount <= 0 {
		workerCount = 1
	}
	if workerCount > len(tasks) {
		workerCount = len(tasks)
	}

	jobs := make(chan deliveryTask)
	var wg sync.WaitGroup
	var resultMu sync.Mutex

	successGuilds := make([]int64, 0, len(tasks))
	successUsers := make([]int64, 0, len(tasks))

	for range workerCount {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for task := range jobs {
				if err := r.executeDelivery(task); err != nil {
					if task.guildID != 0 {
						log.Printf("Send failed for guild %d: %v", task.guildID, err)
					} else {
						log.Printf("DM failed for %d: %v", task.userID, err)
					}
					continue
				}

				resultMu.Lock()
				if task.guildID != 0 {
					successGuilds = append(successGuilds, task.guildID)
				}
				if task.userID != 0 {
					successUsers = append(successUsers, task.userID)
				}
				resultMu.Unlock()
			}
		}()
	}

outer:
	for _, task := range tasks {
		select {
		case <-ctx.Done():
			break outer
		case jobs <- task:
		}
	}

	close(jobs)
	wg.Wait()
	return successGuilds, successUsers
}

func (r *Runtime) executeDelivery(task deliveryTask) error {
	channelID := task.channelID
	if task.userID != 0 {
		channel, err := r.session.UserChannelCreate(strconv.FormatInt(task.userID, 10))
		if err != nil {
			return fmt.Errorf("create DM channel: %w", err)
		}
		channelID = channel.ID
	}

	return r.sendAssetMessage(channelID, task.content, task.attachments)
}

func (r *Runtime) markRecipientsShown(guildIDs, userIDs []int64) {
	if len(guildIDs) == 0 && len(userIDs) == 0 {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	changed := false
	for _, guildID := range guildIDs {
		cfg := r.guildConfigs[guildID]
		if cfg == nil || cfg.ShownAssets {
			continue
		}
		cfg.ShownAssets = true
		changed = true
	}
	for _, userID := range userIDs {
		profile := r.userProfiles[userID]
		if profile == nil || profile.ShownAssets {
			continue
		}
		profile.ShownAssets = true
		changed = true
	}
	if changed {
		r.markStateDirtyLocked()
	}
}

func (r *Runtime) getAttachments(ctx context.Context, assets []model.Asset) ([]Attachment, error) {
	signature := attachmentSignature(assets)
	if signature == "" {
		return nil, nil
	}

	r.mu.RLock()
	if r.attachmentCache.signature == signature && r.attachmentCache.attachments != nil {
		cached := r.attachmentCache.attachments
		r.mu.RUnlock()
		return cached, nil
	}
	r.mu.RUnlock()

	built, err := r.buildAttachments(ctx, assets)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	if r.attachmentCache.signature == signature && r.attachmentCache.attachments != nil {
		cached := r.attachmentCache.attachments
		r.mu.Unlock()
		return cached, nil
	}
	r.attachmentCache = attachmentCache{
		signature:   signature,
		attachments: built,
	}
	r.mu.Unlock()

	return built, nil
}

func (r *Runtime) buildAttachments(ctx context.Context, assets []model.Asset) ([]Attachment, error) {
	tasks := make([]imageTask, 0, len(assets))
	for _, asset := range assets {
		if asset.Image == nil || strings.TrimSpace(*asset.Image) == "" {
			continue
		}
		tasks = append(tasks, imageTask{
			index: len(tasks),
			name:  derefString(asset.Name, "image"),
			url:   strings.TrimSpace(*asset.Image),
		})
	}
	if len(tasks) == 0 {
		return nil, nil
	}

	workerCount := r.imageFetchWorkers
	if workerCount <= 0 {
		workerCount = 1
	}
	if workerCount > len(tasks) {
		workerCount = len(tasks)
	}

	jobs := make(chan imageTask)
	results := make([]Attachment, len(tasks))
	ready := make([]bool, len(tasks))
	var wg sync.WaitGroup

	for range workerCount {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for task := range jobs {
				attachment, err := r.downloadAttachment(ctx, task.url, task.name)
				if err != nil {
					log.Printf("Image fetch failed for %s: %v", task.url, err)
					continue
				}
				results[task.index] = attachment
				ready[task.index] = true
			}
		}()
	}

outer:
	for _, task := range tasks {
		select {
		case <-ctx.Done():
			break outer
		case jobs <- task:
		}
	}

	close(jobs)
	wg.Wait()

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	attachments := make([]Attachment, 0, len(tasks))
	for idx, ok := range ready {
		if !ok {
			continue
		}
		attachments = append(attachments, results[idx])
	}
	return attachments, nil
}

func (r *Runtime) downloadAttachment(ctx context.Context, imageURL, name string) (Attachment, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
	if err != nil {
		return Attachment{}, fmt.Errorf("build request: %w", err)
	}

	resp, err := r.http.Do(req)
	if err != nil {
		return Attachment{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Attachment{}, fmt.Errorf("unexpected status %s", resp.Status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, r.attachmentMaxBytes+1))
	if err != nil {
		return Attachment{}, fmt.Errorf("read body: %w", err)
	}
	if int64(len(body)) > r.attachmentMaxBytes {
		return Attachment{}, fmt.Errorf("attachment exceeds %d bytes", r.attachmentMaxBytes)
	}

	filename := safeFilename(name) + ".png"
	return Attachment{Filename: filename, Content: body}, nil
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
	builder.Grow(len(assets)*96 + 128)
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
	r.mu.RLock()
	deadline := r.deadline
	r.mu.RUnlock()

	switch {
	case deadline.Raw != nil:
		return *deadline.Raw
	case deadline.Structured != nil:
		return localizer.T("deadline.until", map[string]any{
			"day":        deadline.Structured.Day,
			"month_name": localizer.MonthName(deadline.Structured.Month, "format"),
			"hour":       deadline.Structured.Hour,
			"minute":     deadline.Structured.Minute,
			"gmt_offset": deadline.Structured.GMTOffset,
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
	r.mu.RLock()
	defer r.mu.RUnlock()

	if cfg := r.guildConfigs[guildID]; cfg != nil {
		return cfg.Locale
	}
	return r.baseLocale
}

func (r *Runtime) userLocale(userID int64) string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if profile := r.userProfiles[userID]; profile != nil {
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

func (r *Runtime) guildConfigLocked(guildID int64) *model.GuildConfig {
	return r.guildConfigs[guildID]
}

func (r *Runtime) ensureGuildConfigLocked(guildID int64) *model.GuildConfig {
	if cfg := r.guildConfigLocked(guildID); cfg != nil {
		return cfg
	}
	cfg := &model.GuildConfig{
		GuildID:       guildID,
		Locale:        r.baseLocale,
		Enabled:       false,
		IncludeImages: true,
	}
	r.guildConfigs[guildID] = cfg
	return cfg
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

func (r *Runtime) userProfileLocked(userID int64) *model.UserProfile {
	return r.userProfiles[userID]
}

func (r *Runtime) ensureUserProfileLocked(userID int64) *model.UserProfile {
	if profile := r.userProfileLocked(userID); profile != nil {
		return profile
	}
	profile := &model.UserProfile{
		ID:          userID,
		Locale:      r.baseLocale,
		ShownAssets: false,
		Subscribed:  false,
	}
	r.userProfiles[userID] = profile
	return profile
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

func (r *Runtime) enabledGuildConfigsLocked() []model.GuildConfig {
	result := make([]model.GuildConfig, 0, len(r.guildConfigs))
	for _, cfg := range r.guildConfigs {
		if cfg.Enabled && cfg.ChannelID != nil {
			result = append(result, cloneGuildConfig(*cfg))
		}
	}
	slices.SortFunc(result, func(left, right model.GuildConfig) int {
		switch {
		case left.GuildID < right.GuildID:
			return -1
		case left.GuildID > right.GuildID:
			return 1
		default:
			return 0
		}
	})
	return result
}

func (r *Runtime) subscribedUsersLocked() []model.UserProfile {
	result := make([]model.UserProfile, 0, len(r.userProfiles))
	for _, profile := range r.userProfiles {
		if profile.Subscribed {
			result = append(result, *profile)
		}
	}
	slices.SortFunc(result, func(left, right model.UserProfile) int {
		switch {
		case left.ID < right.ID:
			return -1
		case left.ID > right.ID:
			return 1
		default:
			return 0
		}
	})
	return result
}

func (r *Runtime) sendCurrentAssetsToGuild(ctx context.Context, guildID int64) bool {
	cfg, assets, ok := r.guildDeliveryState(guildID)
	if !ok {
		return false
	}

	channelID := r.targetChannelID(cfg)
	if channelID == "" {
		log.Printf("Configured target for guild %d could not be resolved.", cfg.GuildID)
		return false
	}

	message := r.composeMessage(assets, r.localizerForLocale(cfg.Locale))

	var attachments []Attachment
	if cfg.IncludeImages {
		built, err := r.getAttachments(ctx, assets)
		if err != nil {
			log.Printf("attachment build failed: %v", err)
		} else {
			attachments = built
		}
	}

	if err := r.sendAssetMessage(channelID, r.composeDeliveryContent(cfg, message), attachments); err != nil {
		log.Printf("Initial asset send failed for guild %d: %v", cfg.GuildID, err)
		return false
	}

	if r.markGuildShown(guildID) {
		r.requestFlush()
	}
	return true
}

func (r *Runtime) guildDeliveryState(guildID int64) (model.GuildConfig, []model.Asset, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	cfg := r.guildConfigs[guildID]
	if cfg == nil || !cfg.Enabled || len(r.assets) == 0 || cfg.ShownAssets {
		return model.GuildConfig{}, nil, false
	}
	return cloneGuildConfig(*cfg), cloneAssets(r.assets), true
}

func (r *Runtime) sendCurrentAssetsToUser(ctx context.Context, userID int64) bool {
	profile, assets, ok := r.userDeliveryState(userID)
	if !ok {
		return false
	}

	attachments, err := r.getAttachments(ctx, assets)
	if err != nil {
		log.Printf("attachment build failed: %v", err)
	}

	body := r.composeMessage(assets, r.localizerForLocale(profile.Locale))
	dmChannel, err := r.session.UserChannelCreate(strconv.FormatInt(userID, 10))
	if err != nil {
		log.Printf("DM channel create failed for %d: %v", userID, err)
		return false
	}
	if err := r.sendAssetMessage(dmChannel.ID, body, attachments); err != nil {
		log.Printf("DM failed for %d: %v", userID, err)
		return false
	}

	if r.markUserShown(userID) {
		r.requestFlush()
	}
	return true
}

func (r *Runtime) userDeliveryState(userID int64) (model.UserProfile, []model.Asset, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	profile := r.userProfiles[userID]
	if profile == nil || !profile.Subscribed || len(r.assets) == 0 || profile.ShownAssets {
		return model.UserProfile{}, nil, false
	}
	return *profile, cloneAssets(r.assets), true
}

func (r *Runtime) markGuildShown(guildID int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	cfg := r.guildConfigs[guildID]
	if cfg == nil || cfg.ShownAssets {
		return false
	}
	cfg.ShownAssets = true
	r.markStateDirtyLocked()
	return true
}

func (r *Runtime) markUserShown(userID int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	profile := r.userProfiles[userID]
	if profile == nil || profile.ShownAssets {
		return false
	}
	profile.ShownAssets = true
	r.markStateDirtyLocked()
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
	r.mu.RLock()
	nextCheck := r.nextCheck
	r.mu.RUnlock()

	if nextCheck.IsZero() {
		return localizer.T("server.settings.no_next_check", nil)
	}
	remaining := time.Until(nextCheck)
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

	missing := make([]string, 0, 2)
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

func guildConfigMap(input []model.GuildConfig) map[int64]*model.GuildConfig {
	output := make(map[int64]*model.GuildConfig, len(input))
	for _, cfg := range input {
		cloned := cloneGuildConfig(cfg)
		output[cfg.GuildID] = &cloned
	}
	return output
}

func userProfileMap(input []model.UserProfile) map[int64]*model.UserProfile {
	output := make(map[int64]*model.UserProfile, len(input))
	for _, profile := range input {
		cloned := profile
		output[profile.ID] = &cloned
	}
	return output
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

func cloneGuildConfigMap(input map[int64]*model.GuildConfig) []model.GuildConfig {
	output := make([]model.GuildConfig, 0, len(input))
	for _, cfg := range input {
		output = append(output, cloneGuildConfig(*cfg))
	}
	slices.SortFunc(output, func(left, right model.GuildConfig) int {
		switch {
		case left.GuildID < right.GuildID:
			return -1
		case left.GuildID > right.GuildID:
			return 1
		default:
			return 0
		}
	})
	return output
}

func cloneGuildConfig(cfg model.GuildConfig) model.GuildConfig {
	cloned := cfg
	cloned.ChannelID = cloneInt64Ptr(cfg.ChannelID)
	cloned.ThreadID = cloneInt64Ptr(cfg.ThreadID)
	cloned.MentionRoleID = cloneInt64Ptr(cfg.MentionRoleID)
	return cloned
}

func cloneUserProfileMap(input map[int64]*model.UserProfile) []model.UserProfile {
	output := make([]model.UserProfile, 0, len(input))
	for _, profile := range input {
		output = append(output, *profile)
	}
	slices.SortFunc(output, func(left, right model.UserProfile) int {
		switch {
		case left.ID < right.ID:
			return -1
		case left.ID > right.ID:
			return 1
		default:
			return 0
		}
	})
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

func attachmentSignature(assets []model.Asset) string {
	hasher := fnv.New64a()
	hasImage := false

	for _, asset := range assets {
		if asset.Image == nil || strings.TrimSpace(*asset.Image) == "" {
			continue
		}
		hasImage = true
		_, _ = hasher.Write([]byte(derefString(asset.Name, "")))
		_, _ = hasher.Write([]byte{0})
		_, _ = hasher.Write([]byte(asset.Link))
		_, _ = hasher.Write([]byte{0})
		_, _ = hasher.Write([]byte(*asset.Image))
		_, _ = hasher.Write([]byte{0})
	}

	if !hasImage {
		return ""
	}
	return strconv.FormatUint(hasher.Sum64(), 16)
}

func safeFilename(name string) string {
	safe := invalidFilenamePattern.ReplaceAllString(strings.TrimSpace(name), "_")
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

func stopTimer(timer *time.Timer) {
	if timer == nil {
		return
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}

func isContextDone(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
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
