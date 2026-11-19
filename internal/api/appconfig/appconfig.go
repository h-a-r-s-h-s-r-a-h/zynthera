// Package appconfig is the typed configuration of zynthera-api.
package appconfig

import (
	"net/url"
	"strings"

	"zynthera/internal/shared/config"
)

// Config holds every setting of the control plane (spec section 8.2).
type Config struct {
	Env         string // development | production
	Addr        string
	DatabaseURL string
	RedisURL    string
	WebOrigin   string
	SecretsKey  []byte

	SessionTTLHours int

	SMTPHost string
	SMTPPort int
	SMTPUser string
	SMTPPass string
	MailFrom string

	SeedAdminEmail    string
	SeedAdminPassword string

	AllowedRAMMB  []int
	AllowedVCPUs  []int
	AllowedDiskGB []int

	DefaultMaxVMs    int
	DefaultMaxRAMMB  int
	DefaultMaxVCPUs  int
	DefaultMaxDiskGB int

	IdleSuspendMinutes      int
	IdleInputMinutes        int
	MaxKeepAliveHours       int
	MaxSnapshotsPerVM       int
	MaxUploadMB             int
	StreamTokenTTLSeconds   int
	FilesTokenTTLSeconds    int
	HostOfflineAfterSeconds int
	DiskHeadroomPercent     int

	// Development host, used by `seed` (spec section 7.4).
	AgentHostID       string
	AgentToken        string
	AgentStreamSecret string
	AgentAddr         string
}

// IsProduction reports whether the API runs for real users.
func (c Config) IsProduction() bool { return c.Env == "production" }

// Load reads and validates the configuration from the environment.
func Load() (Config, error) {
	var l config.Loader
	c := Config{
		Env:         l.OneOf("ZYN_ENV", "development", "development", "production"),
		Addr:        l.String("ZYN_API_ADDR", "127.0.0.1:8080"),
		DatabaseURL: l.Required("ZYN_DATABASE_URL"),
		RedisURL:    l.Required("ZYN_REDIS_URL"),
		WebOrigin:   strings.TrimRight(l.Required("ZYN_WEB_ORIGIN"), "/"),
		SecretsKey:  l.Key32("ZYN_SECRETS_KEY"),

		SessionTTLHours: l.Int("ZYN_SESSION_TTL_HOURS", 720),

		SMTPHost: l.String("ZYN_SMTP_HOST", "127.0.0.1"),
		SMTPPort: l.Int("ZYN_SMTP_PORT", 1025),
		SMTPUser: l.String("ZYN_SMTP_USER", ""),
		SMTPPass: l.String("ZYN_SMTP_PASS", ""),
		MailFrom: l.String("ZYN_MAIL_FROM", "Zynthera <no-reply@zynthera.local>"),

		SeedAdminEmail:    l.String("ZYN_SEED_ADMIN_EMAIL", "admin@zynthera.local"),
		SeedAdminPassword: l.String("ZYN_SEED_ADMIN_PASSWORD", ""),

		AllowedRAMMB:  l.IntList("ZYN_ALLOWED_RAM_MB", []int{2048, 4096, 8192, 16384}),
		AllowedVCPUs:  l.IntList("ZYN_ALLOWED_VCPUS", []int{1, 2, 4, 8}),
		AllowedDiskGB: l.IntList("ZYN_ALLOWED_DISK_GB", []int{40, 80, 160}),

		DefaultMaxVMs:    l.Int("ZYN_DEFAULT_MAX_VMS", 1),
		DefaultMaxRAMMB:  l.Int("ZYN_DEFAULT_MAX_RAM_MB", 16384),
		DefaultMaxVCPUs:  l.Int("ZYN_DEFAULT_MAX_VCPUS", 8),
		DefaultMaxDiskGB: l.Int("ZYN_DEFAULT_MAX_DISK_GB", 160),

		IdleSuspendMinutes:      l.Int("ZYN_IDLE_SUSPEND_MINUTES", 15),
		IdleInputMinutes:        l.Int("ZYN_IDLE_INPUT_MINUTES", 60),
		MaxKeepAliveHours:       l.Int("ZYN_MAX_KEEP_ALIVE_HOURS", 12),
		MaxSnapshotsPerVM:       l.Int("ZYN_MAX_SNAPSHOTS_PER_VM", 5),
		MaxUploadMB:             l.Int("ZYN_MAX_UPLOAD_MB", 2048),
		StreamTokenTTLSeconds:   l.Int("ZYN_STREAM_TOKEN_TTL_SECONDS", 60),
		FilesTokenTTLSeconds:    l.Int("ZYN_FILES_TOKEN_TTL_SECONDS", 600),
		HostOfflineAfterSeconds: l.Int("ZYN_HOST_OFFLINE_AFTER_SECONDS", 45),
		DiskHeadroomPercent:     l.Int("ZYN_DISK_HEADROOM_PERCENT", 10),

		AgentHostID:       l.String("ZYN_AGENT_HOST_ID", ""),
		AgentToken:        l.String("ZYN_AGENT_TOKEN", ""),
		AgentStreamSecret: l.String("ZYN_AGENT_STREAM_SECRET", ""),
		AgentAddr:         l.String("ZYN_AGENT_ADDR", "127.0.0.1:9090"),
	}

	if c.WebOrigin != "" {
		u, err := url.Parse(c.WebOrigin)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Path != "" {
			l.Fail("ZYN_WEB_ORIGIN must look like https://app.example.com with no path, got %q", c.WebOrigin)
		}
	}
	if c.DiskHeadroomPercent >= 100 {
		l.Fail("ZYN_DISK_HEADROOM_PERCENT must be below 100")
	}

	if c.IsProduction() {
		l.NotPlaceholder("ZYN_SECRETS_KEY", c.SecretsKey)
		l.NotPlaceholder("ZYN_SEED_ADMIN_PASSWORD", []byte(c.SeedAdminPassword))
		if strings.Contains(c.DatabaseURL, "zynthera_dev") {
			l.Fail("ZYN_DATABASE_URL still uses the development password")
		}
		if strings.HasPrefix(c.WebOrigin, "http://") {
			l.Fail("ZYN_WEB_ORIGIN must use https in production")
		}
	}

	return c, l.Err()
}
