package config

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"paasctl/internal/secrets"
)

const ConfigFilePath = "~/.config/paastctl/config.yaml"

// Config holds file- and environment-driven configuration for the CLI.
type Config struct {
	WhiteSky WhiteSkyConfig
	Elestio  ElestioConfig
}

type WhiteSkyConfig struct {
	BaseURL        string
	IAMBaseURL     string
	Token          string
	CustomerID     string
	CloudspaceID   string
	Cloudspaces    map[string]CloudspaceConfig
	RequestTimeout time.Duration
}

type CloudspaceConfig struct {
	CloudspaceID string `json:"cloudspace_id"`
}

type ElestioConfig struct {
	BaseURL            string
	Email              string
	APIToken           string
	ProjectID          string
	BYOVMPricePerHour  float64
	BYOVMProviderLabel string
}

var configurableKeys = []string{
	"whitesky.base_url",
	"whitesky.iam_base_url",
	"whitesky.token",
	"whitesky.customer_id",
	"whitesky.cloudspaces.<name>.cloudspace_id",
	"whitesky.request_timeout",
	"paas-providers.elestio.base_url",
	"paas-providers.elestio.email",
	"paas-providers.elestio.api_token",
	"paas-providers.elestio.project_id",
	"paas-providers.elestio.byovm_price_per_hour",
	"paas-providers.elestio.byovm_provider_label",
}

func Load() Config {
	cfg := Config{
		WhiteSky: WhiteSkyConfig{
			BaseURL:        "https://try.whitesky.cloud/api/1",
			Cloudspaces:    make(map[string]CloudspaceConfig),
			RequestTimeout: 300 * time.Second,
		},
		Elestio: ElestioConfig{
			BaseURL:            "https://api.elest.io",
			BYOVMProviderLabel: "whitesky.cloud",
		},
	}

	if fileCfg, err := loadFileConfig(DefaultConfigFilePath()); err == nil {
		cfg.applyFile(fileCfg)
	}

	cfg.WhiteSky.BaseURL = envOrDefault("PAASCTL_WHITESKY_BASE_URL", cfg.WhiteSky.BaseURL)
	cfg.WhiteSky.IAMBaseURL = envOrDefault("PAASCTL_WHITESKY_IAM_BASE_URL", cfg.WhiteSky.IAMBaseURL)
	cfg.WhiteSky.Token = envOrDefault("PAASCTL_WHITESKY_TOKEN", cfg.WhiteSky.Token)
	cfg.WhiteSky.CustomerID = envOrDefault("PAASCTL_WHITESKY_CUSTOMER_ID", cfg.WhiteSky.CustomerID)
	cfg.WhiteSky.CloudspaceID = envOrDefault("PAASCTL_WHITESKY_CLOUDSPACE_ID", cfg.WhiteSky.CloudspaceID)
	if cfg.WhiteSky.CloudspaceID != "" && len(cfg.WhiteSky.Cloudspaces) == 0 {
		cfg.WhiteSky.Cloudspaces["default"] = CloudspaceConfig{CloudspaceID: cfg.WhiteSky.CloudspaceID}
	}
	cfg.WhiteSky.RequestTimeout = envDurationOrDefault("PAASCTL_WHITESKY_REQUEST_TIMEOUT", cfg.WhiteSky.RequestTimeout)

	cfg.Elestio.BaseURL = envOrDefault("PAASCTL_ELESTIO_BASE_URL", cfg.Elestio.BaseURL)
	cfg.Elestio.Email = envOrDefault("PAASCTL_ELESTIO_EMAIL", cfg.Elestio.Email)
	cfg.Elestio.APIToken = envOrDefault("PAASCTL_ELESTIO_API_TOKEN", cfg.Elestio.APIToken)
	cfg.Elestio.ProjectID = envOrDefault("PAASCTL_ELESTIO_PROJECT_ID", cfg.Elestio.ProjectID)
	cfg.Elestio.BYOVMPricePerHour = envFloatOrDefault("PAASCTL_ELESTIO_BYOVM_PRICE_PER_HOUR", cfg.Elestio.BYOVMPricePerHour)
	cfg.Elestio.BYOVMProviderLabel = envOrDefault("PAASCTL_ELESTIO_BYOVM_PROVIDER_LABEL", cfg.Elestio.BYOVMProviderLabel)

	return cfg
}

func ConfigurableKeys() []string {
	out := append([]string(nil), configurableKeys...)
	sort.Strings(out)
	return out
}

func (c Config) ResolveWhiteSky(selector string) (WhiteSkyConfig, string, error) {
	cfg := c.WhiteSky
	if strings.TrimSpace(cfg.Token) == "" {
		return WhiteSkyConfig{}, "", fmt.Errorf("missing required config PAASCTL_WHITESKY_TOKEN or whitesky.token")
	}
	if strings.TrimSpace(cfg.CustomerID) == "" {
		return WhiteSkyConfig{}, "", fmt.Errorf("missing required config PAASCTL_WHITESKY_CUSTOMER_ID or whitesky.customer_id")
	}

	name := strings.TrimSpace(selector)
	if name != "" {
		if cs, ok := cfg.Cloudspaces[name]; ok {
			cfg.CloudspaceID = strings.TrimSpace(cs.CloudspaceID)
			return cfg, name, validateResolvedCloudspace(cfg.CloudspaceID, name)
		}
		cfg.CloudspaceID = name
		return cfg, "", validateResolvedCloudspace(cfg.CloudspaceID, name)
	}

	if strings.TrimSpace(cfg.CloudspaceID) != "" {
		return cfg, "default", nil
	}
	if len(cfg.Cloudspaces) == 1 {
		for configuredName, cs := range cfg.Cloudspaces {
			cfg.CloudspaceID = strings.TrimSpace(cs.CloudspaceID)
			return cfg, configuredName, validateResolvedCloudspace(cfg.CloudspaceID, configuredName)
		}
	}
	if len(cfg.Cloudspaces) > 1 {
		return WhiteSkyConfig{}, "", fmt.Errorf("multiple cloudspaces configured; pass --cloudspace <name>")
	}
	return WhiteSkyConfig{}, "", fmt.Errorf("missing cloudspace; configure whitesky.cloudspaces.<name>.cloudspace_id or pass --cloudspace <id>")
}

func SetFileValue(key, value string) error {
	section, itemKey, err := splitConfigPath(key)
	if err != nil {
		return err
	}
	if err := validateFileValue(section, itemKey, value); err != nil {
		return err
	}

	path := DefaultConfigFilePath()
	values, err := readExistingFileValues(path)
	if err != nil {
		return err
	}
	if values[section] == nil {
		values[section] = make(map[string]string)
	}
	if isSensitiveFileKey(section, itemKey) {
		value, err = secrets.EncryptString(value)
		if err != nil {
			return fmt.Errorf("failed to encrypt %s.%s: %w", section, itemKey, err)
		}
	}
	values[section][itemKey] = value
	if err := encryptSensitiveValues(values); err != nil {
		return err
	}
	return writeFileConfig(path, values)
}

func Unlock(password string) error {
	return secrets.Unlock(password)
}

func Relock() error {
	return secrets.Relock()
}

func ReadPassword(prompt string) (string, error) {
	return secrets.ReadPassword(prompt)
}

func validateResolvedCloudspace(cloudspaceID, selector string) error {
	if strings.TrimSpace(cloudspaceID) == "" {
		return fmt.Errorf("cloudspace %q has no cloudspace_id", selector)
	}
	return nil
}

func UnsetFileValue(key string) error {
	section, itemKey, err := splitConfigPath(key)
	if err != nil {
		return err
	}

	path := DefaultConfigFilePath()
	values, err := readExistingFileValues(path)
	if err != nil {
		return err
	}
	if values[section] != nil {
		delete(values[section], itemKey)
	}
	if err := encryptSensitiveValues(values); err != nil {
		return err
	}
	return writeFileConfig(path, values)
}

func (c Config) ValidateWhiteSky() error {
	_, _, err := c.ResolveWhiteSky("")
	return err
}

func (c Config) ValidateWhiteSkyCredentials() error {
	if c.WhiteSky.Token == "" {
		return fmt.Errorf("missing required config PAASCTL_WHITESKY_TOKEN or whitesky.token")
	}
	if c.WhiteSky.CustomerID == "" {
		return fmt.Errorf("missing required config PAASCTL_WHITESKY_CUSTOMER_ID or whitesky.customer_id")
	}
	return nil
}

func (c Config) ValidateElestio() error {
	if c.Elestio.Email == "" {
		return fmt.Errorf("missing required config PAASCTL_ELESTIO_EMAIL or paas-providers.elestio.email")
	}
	if c.Elestio.APIToken == "" {
		return fmt.Errorf("missing required config PAASCTL_ELESTIO_API_TOKEN or paas-providers.elestio.api_token")
	}
	return nil
}

func DefaultConfigFilePath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ConfigFilePath
	}
	return filepath.Join(home, ".config", "paastctl", "config.yaml")
}

func (c *Config) applyFile(values map[string]map[string]string) {
	ws := values["whitesky"]
	c.WhiteSky.BaseURL = stringValue(ws, "base_url", c.WhiteSky.BaseURL)
	c.WhiteSky.IAMBaseURL = stringValue(ws, "iam_base_url", c.WhiteSky.IAMBaseURL)
	c.WhiteSky.Token = secretStringValue(ws, "token", c.WhiteSky.Token)
	c.WhiteSky.CustomerID = stringValue(ws, "customer_id", c.WhiteSky.CustomerID)
	c.WhiteSky.CloudspaceID = stringValue(ws, "cloudspace_id", c.WhiteSky.CloudspaceID)
	c.WhiteSky.RequestTimeout = durationValue(ws, "request_timeout", c.WhiteSky.RequestTimeout)
	if c.WhiteSky.Cloudspaces == nil {
		c.WhiteSky.Cloudspaces = make(map[string]CloudspaceConfig)
	}
	for section, sectionValues := range values {
		const prefix = "whitesky.cloudspaces."
		if !strings.HasPrefix(section, prefix) {
			continue
		}
		name := strings.TrimSpace(strings.TrimPrefix(section, prefix))
		if name == "" {
			continue
		}
		if id := stringValue(sectionValues, "cloudspace_id", ""); id != "" {
			c.WhiteSky.Cloudspaces[name] = CloudspaceConfig{CloudspaceID: id}
		}
	}

	el := values["paas_providers.elestio"]
	c.Elestio.BaseURL = stringValue(el, "base_url", c.Elestio.BaseURL)
	c.Elestio.Email = stringValue(el, "email", c.Elestio.Email)
	c.Elestio.APIToken = secretStringValue(el, "api_token", c.Elestio.APIToken)
	c.Elestio.ProjectID = stringValue(el, "project_id", c.Elestio.ProjectID)
	c.Elestio.BYOVMPricePerHour = floatValue(el, "byovm_price_per_hour", c.Elestio.BYOVMPricePerHour)
	c.Elestio.BYOVMProviderLabel = stringValue(el, "byovm_provider_label", c.Elestio.BYOVMProviderLabel)
}

func loadFileConfig(path string) (map[string]map[string]string, error) {
	raw, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseSimpleYAML(raw)
}

func readExistingFileValues(path string) (map[string]map[string]string, error) {
	values, err := loadFileConfig(path)
	if err == nil {
		normalizeFileValues(values)
		return values, nil
	}
	if os.IsNotExist(err) {
		return make(map[string]map[string]string), nil
	}
	return nil, err
}

func normalizeFileValues(values map[string]map[string]string) {
	ws := values["whitesky"]
	if ws == nil {
		return
	}
	if id := strings.TrimSpace(ws["cloudspace_id"]); id != "" {
		section := "whitesky.cloudspaces.default"
		if values[section] == nil {
			values[section] = make(map[string]string)
		}
		if strings.TrimSpace(values[section]["cloudspace_id"]) == "" {
			values[section]["cloudspace_id"] = id
		}
		delete(ws, "cloudspace_id")
	}
}

func writeFileConfig(path string, values map[string]map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return ioutil.WriteFile(path, []byte(renderFileConfig(values)), 0600)
}

func renderFileConfig(values map[string]map[string]string) string {
	var b strings.Builder

	ws := values["whitesky"]
	cloudspaceSections := matchingSections(values, "whitesky.cloudspaces.")
	if len(ws) > 0 || len(cloudspaceSections) > 0 {
		b.WriteString("whitesky:\n")
		for _, key := range orderedPresentKeys(ws, []string{"base_url", "iam_base_url", "token", "customer_id", "request_timeout"}) {
			writeYAMLKeyValue(&b, 1, key, ws[key])
		}
		if len(cloudspaceSections) > 0 {
			b.WriteString("  cloudspaces:\n")
			for _, section := range cloudspaceSections {
				name := strings.TrimPrefix(section, "whitesky.cloudspaces.")
				b.WriteString("    ")
				b.WriteString(name)
				b.WriteString(":\n")
				for _, key := range orderedPresentKeys(values[section], []string{"cloudspace_id"}) {
					writeYAMLKeyValue(&b, 3, key, values[section][key])
				}
			}
		}
		b.WriteString("\n")
	}

	if el := values["paas_providers.elestio"]; len(el) > 0 {
		b.WriteString("paas-providers:\n")
		b.WriteString("  elestio:\n")
		for _, key := range orderedPresentKeys(el, []string{"base_url", "email", "api_token", "project_id", "byovm_price_per_hour", "byovm_provider_label"}) {
			writeYAMLKeyValue(&b, 2, key, el[key])
		}
	}

	return strings.TrimRight(b.String(), "\n") + "\n"
}

func splitConfigPath(path string) (string, string, error) {
	normalized := normalizeConfigKey(path)
	if !isConfigurableKey(normalized) {
		return "", "", fmt.Errorf("unsupported config key %q; run `paasctl config keys` to list supported keys", path)
	}
	parts := strings.Split(normalized, ".")
	if len(parts) < 2 {
		return "", "", fmt.Errorf("invalid config key %q", path)
	}
	return strings.Join(parts[:len(parts)-1], "."), parts[len(parts)-1], nil
}

func isConfigurableKey(normalized string) bool {
	if strings.HasPrefix(normalized, "whitesky.cloudspaces.") && strings.HasSuffix(normalized, ".cloudspace_id") {
		parts := strings.Split(normalized, ".")
		return len(parts) == 4 && strings.TrimSpace(parts[2]) != ""
	}
	for _, key := range configurableKeys {
		if normalizeConfigKey(key) == normalized {
			return true
		}
	}
	return false
}

func matchingSections(values map[string]map[string]string, prefix string) []string {
	out := make([]string, 0)
	for section := range values {
		if strings.HasPrefix(section, prefix) {
			out = append(out, section)
		}
	}
	sort.Strings(out)
	return out
}

func validateFileValue(section, key, value string) error {
	fullKey := section + "." + key
	switch fullKey {
	case "whitesky.request_timeout":
		if _, err := time.ParseDuration(value); err != nil {
			return fmt.Errorf("invalid duration for %s: %w", fullKey, err)
		}
	case "paas_providers.elestio.byovm_price_per_hour":
		if _, err := strconv.ParseFloat(value, 64); err != nil {
			return fmt.Errorf("invalid float for %s: %w", fullKey, err)
		}
	}
	return nil
}

func isSensitiveFileKey(section, key string) bool {
	switch section + "." + key {
	case "whitesky.token", "paas_providers.elestio.api_token":
		return true
	default:
		return false
	}
}

func encryptSensitiveValues(values map[string]map[string]string) error {
	for _, item := range []struct {
		section string
		key     string
	}{
		{section: "whitesky", key: "token"},
		{section: "paas_providers.elestio", key: "api_token"},
	} {
		sectionValues := values[item.section]
		if sectionValues == nil {
			continue
		}
		value := strings.TrimSpace(sectionValues[item.key])
		if value == "" || secrets.IsEncrypted(value) {
			continue
		}
		encrypted, err := secrets.EncryptString(value)
		if err != nil {
			return fmt.Errorf("failed to encrypt %s.%s: %w", item.section, item.key, err)
		}
		sectionValues[item.key] = encrypted
	}
	return nil
}

func orderedPresentKeys(values map[string]string, preferred []string) []string {
	seen := make(map[string]bool)
	out := make([]string, 0, len(values))
	for _, key := range preferred {
		if _, ok := values[key]; ok {
			out = append(out, key)
			seen[key] = true
		}
	}
	extra := make([]string, 0)
	for key := range values {
		if !seen[key] {
			extra = append(extra, key)
		}
	}
	sort.Strings(extra)
	return append(out, extra...)
}

func writeYAMLKeyValue(b *strings.Builder, indent int, key, value string) {
	for i := 0; i < indent; i++ {
		b.WriteString("  ")
	}
	b.WriteString(key)
	b.WriteString(": ")
	b.WriteString(quoteYAMLString(value))
	b.WriteString("\n")
}

func quoteYAMLString(value string) string {
	escaped := strings.ReplaceAll(value, "\\", "\\\\")
	escaped = strings.ReplaceAll(escaped, "\"", "\\\"")
	return `"` + escaped + `"`
}

func parseSimpleYAML(raw []byte) (map[string]map[string]string, error) {
	out := make(map[string]map[string]string)
	sectionStack := make(map[int]string)
	section := ""
	lines := strings.Split(string(raw), "\n")
	for _, line := range lines {
		line = strings.TrimRight(line, " \t\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		trimmed := strings.TrimSpace(stripComment(line))
		if trimmed == "" {
			continue
		}
		indent := countLeadingWhitespace(line)
		if strings.HasSuffix(trimmed, ":") {
			name := normalizeConfigKey(strings.TrimSuffix(trimmed, ":"))
			level := indent / 2
			sectionStack[level] = name
			for existing := range sectionStack {
				if existing > level {
					delete(sectionStack, existing)
				}
			}
			parts := make([]string, 0, level+1)
			for i := 0; i <= level; i++ {
				if sectionStack[i] != "" {
					parts = append(parts, sectionStack[i])
				}
			}
			section = strings.Join(parts, ".")
			if out[section] == nil {
				out[section] = make(map[string]string)
			}
			continue
		}
		if section == "" {
			continue
		}
		sep := strings.Index(trimmed, ":")
		if sep < 0 {
			continue
		}
		key := trimmed[:sep]
		value := trimmed[sep+1:]
		out[section][normalizeConfigKey(key)] = unquoteYAMLValue(strings.TrimSpace(value))
	}
	return out, nil
}

func countLeadingWhitespace(value string) int {
	count := 0
	for _, r := range value {
		if r != ' ' && r != '\t' {
			return count
		}
		count++
	}
	return count
}

func stripComment(line string) string {
	inSingle := false
	inDouble := false
	for i, r := range line {
		switch r {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '#':
			if !inSingle && !inDouble {
				return line[:i]
			}
		}
	}
	return line
}

func unquoteYAMLValue(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 {
		if (value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'') {
			return value[1 : len(value)-1]
		}
	}
	return value
}

func normalizeConfigKey(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "-", "_")
	return value
}

func stringValue(values map[string]string, key, fallback string) string {
	if values == nil {
		return fallback
	}
	if v := strings.TrimSpace(values[key]); v != "" {
		return v
	}
	return fallback
}

func secretStringValue(values map[string]string, key, fallback string) string {
	if values == nil {
		return fallback
	}
	v := strings.TrimSpace(values[key])
	if v == "" {
		return fallback
	}
	if !secrets.IsEncrypted(v) {
		return v
	}
	decrypted, err := secrets.DecryptString(v)
	if err != nil {
		return ""
	}
	return decrypted
}

func floatValue(values map[string]string, key string, fallback float64) float64 {
	if values == nil || strings.TrimSpace(values[key]) == "" {
		return fallback
	}
	parsed, err := strconv.ParseFloat(strings.TrimSpace(values[key]), 64)
	if err != nil {
		return fallback
	}
	return parsed
}

func durationValue(values map[string]string, key string, fallback time.Duration) time.Duration {
	if values == nil || strings.TrimSpace(values[key]) == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(values[key]))
	if err != nil {
		return fallback
	}
	return parsed
}

func envOrDefault(key, fallback string) string {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	return v
}

func envFloatOrDefault(key string, fallback float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	parsed, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

func envDurationOrDefault(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(v)
	if err != nil {
		return fallback
	}
	return parsed
}
