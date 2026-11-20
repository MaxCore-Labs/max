package platform

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"

	"max/internal/domain"
)

// Config holds validated daemon configuration loaded from environment variables (Spec 8.15).
type Config struct {
	DataDir                string
	VaultDir               string
	Listen                 string
	PublicURL              string
	TZ                     string
	EngineAuth             string
	Engines                []domain.EngineName // empty = auto-detect installed & logged-in CLIs
	Slots                  map[domain.EngineName]int
	QuotaShortHours        int
	QuotaShortTokens       *int64 // nil = learning mode
	QuotaWeekTokens        *int64 // nil = learning mode
	QuotaBackgroundShare   float64
	BrowserSeats           int
	BrowserTZ              string
	BrowserLocale          string
	CloakBrowserLicenseKey string
	AgentMemorySecret      string
	VaultRemote            string
	TelegramBotToken       string
	TelegramOwnerIDs       []int64
	QuietHours             string
	AdminPassword          string
}

func envOrDefault(getenv func(string) string, key, def string) string {
	if v := strings.TrimSpace(getenv(key)); v != "" {
		return v
	}
	return def
}

func parseOptionalInt64(getenv func(string) string, key string) (*int64, error) {
	raw := strings.TrimSpace(getenv(key))
	if raw == "" {
		return nil, nil
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v <= 0 {
		return nil, fmt.Errorf("invalid %s=%q: must be positive integer", key, raw)
	}
	return &v, nil
}

func parseSlots(getenv func(string) string, key string, def int) (int, error) {
	raw := strings.TrimSpace(getenv(key))
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > 8 {
		return 0, fmt.Errorf("invalid %s=%q: must be integer in [1, 8]", key, raw)
	}
	return n, nil
}

// Load parses and validates Max configuration from getenv.
func Load(getenv func(string) string) (Config, error) {
	cfg := Config{
		DataDir:                envOrDefault(getenv, "MAX_DATA_DIR", "./data"),
		VaultDir:               envOrDefault(getenv, "MAX_VAULT_DIR", "./vault"),
		Listen:                 envOrDefault(getenv, "MAX_LISTEN", "127.0.0.1:8787"),
		PublicURL:              envOrDefault(getenv, "MAX_PUBLIC_URL", "http://127.0.0.1:8787"),
		TZ:                     envOrDefault(getenv, "MAX_TZ", "Asia/Ho_Chi_Minh"),
		EngineAuth:             envOrDefault(getenv, "MAX_ENGINE_AUTH", "subscription"),
		Slots:                  make(map[domain.EngineName]int, 3),
		QuotaShortHours:        5,
		QuotaBackgroundShare:   0.7,
		BrowserSeats:           1,
		BrowserTZ:              strings.TrimSpace(getenv("MAX_BROWSER_TZ")),
		BrowserLocale:          envOrDefault(getenv, "MAX_BROWSER_LOCALE", "en-US"),
		CloakBrowserLicenseKey: strings.TrimSpace(getenv("CLOAKBROWSER_LICENSE_KEY")),
		AgentMemorySecret:      strings.TrimSpace(getenv("AGENTMEMORY_SECRET")),
		VaultRemote:            strings.TrimSpace(getenv("MAX_VAULT_REMOTE")),
		TelegramBotToken:       strings.TrimSpace(getenv("MAX_TELEGRAM_BOT_TOKEN")),
		QuietHours:             strings.TrimSpace(getenv("MAX_QUIET_HOURS")),
		AdminPassword:          getenv("MAX_ADMIN_PASSWORD"),
	}

	if _, err := time.LoadLocation(cfg.TZ); err != nil {
		return Config{}, fmt.Errorf("invalid MAX_TZ=%q: %w", cfg.TZ, err)
	}
	if cfg.EngineAuth != "subscription" && cfg.EngineAuth != "api_key" {
		return Config{}, fmt.Errorf("invalid MAX_ENGINE_AUTH=%q: must be subscription or api_key", cfg.EngineAuth)
	}

	if rawEngines := strings.TrimSpace(getenv("MAX_ENGINES")); rawEngines != "" {
		seen := make(map[domain.EngineName]bool, 3)
		for part := range strings.SplitSeq(rawEngines, ",") {
			e := domain.EngineName(strings.TrimSpace(part))
			switch e {
			case domain.EngineAgy, domain.EngineClaude, domain.EngineCodex:
				if !seen[e] {
					seen[e] = true
					cfg.Engines = append(cfg.Engines, e)
				}
			default:
				return Config{}, fmt.Errorf("invalid MAX_ENGINES entry %q", part)
			}
		}
	}

	slotVars := []struct {
		engine domain.EngineName
		envKey string
	}{
		{domain.EngineAgy, "MAX_SLOTS_AGY"},
		{domain.EngineClaude, "MAX_SLOTS_CLAUDE"},
		{domain.EngineCodex, "MAX_SLOTS_CODEX"},
	}
	for _, sv := range slotVars {
		n, err := parseSlots(getenv, sv.envKey, 3)
		if err != nil {
			return Config{}, err
		}
		cfg.Slots[sv.engine] = n
	}

	if rawHours := strings.TrimSpace(getenv("MAX_QUOTA_SHORT_HOURS")); rawHours != "" {
		h, err := strconv.Atoi(rawHours)
		if err != nil || h < 1 || h > 168 {
			return Config{}, fmt.Errorf("invalid MAX_QUOTA_SHORT_HOURS=%q: must be in [1, 168]", rawHours)
		}
		cfg.QuotaShortHours = h
	}

	var err error
	if cfg.QuotaShortTokens, err = parseOptionalInt64(getenv, "MAX_QUOTA_SHORT_TOKENS"); err != nil {
		return Config{}, err
	}
	if cfg.QuotaWeekTokens, err = parseOptionalInt64(getenv, "MAX_QUOTA_WEEK_TOKENS"); err != nil {
		return Config{}, err
	}

	if rawShare := strings.TrimSpace(getenv("MAX_QUOTA_BACKGROUND_SHARE")); rawShare != "" {
		sh, sErr := strconv.ParseFloat(rawShare, 64)
		if sErr != nil || sh <= 0 || sh > 1.0 {
			return Config{}, fmt.Errorf("invalid MAX_QUOTA_BACKGROUND_SHARE=%q: must be in (0, 1]", rawShare)
		}
		cfg.QuotaBackgroundShare = sh
	}

	if rawSeats := strings.TrimSpace(getenv("MAX_BROWSER_SEATS")); rawSeats != "" {
		bs, bErr := strconv.Atoi(rawSeats)
		if bErr != nil || bs < 1 || bs > 2000 {
			return Config{}, fmt.Errorf("invalid MAX_BROWSER_SEATS=%q: must be >= 1", rawSeats)
		}
		cfg.BrowserSeats = bs
	}

	if rawOwners := strings.TrimSpace(getenv("MAX_TELEGRAM_OWNER_IDS")); rawOwners != "" {
		for part := range strings.SplitSeq(rawOwners, ",") {
			p := strings.TrimSpace(part)
			if p == "" {
				continue
			}
			id, pErr := strconv.ParseInt(p, 10, 64)
			if pErr != nil {
				return Config{}, fmt.Errorf("invalid MAX_TELEGRAM_OWNER_IDS entry %q: %w", p, pErr)
			}
			cfg.TelegramOwnerIDs = append(cfg.TelegramOwnerIDs, id)
		}
	}

	return cfg, nil
}
