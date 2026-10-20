package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type database struct {
	alias       string
	service     string
	passwordEnv string
	host        string
	port        int
	name        string
	user        string
	password    string
	sslMode     string
}

type settings struct {
	executionMode  string
	composeFile    string
	composeDir     string
	composeEnvFile string
	project        string
	directory      string
	databases      []database
	provider       string
	bucket         string
	prefix         string
	endpointURL    string
	retentionCount int
	interval       int
	runOnStart     bool
}

func loadSettings() (settings, error) {
	project := env("BACKUP_PROJECT", env("PROJECT_NAME", "project"))
	if !projectPattern.MatchString(project) || project == "." || project == ".." {
		return settings{}, errorsf("BACKUP_PROJECT may contain only letters, numbers, dots, underscores, and hyphens")
	}
	aliases := splitCSV(env("BACKUP_DATABASES", ""))
	if len(aliases) == 0 {
		return settings{}, errorsf("BACKUP_DATABASES must list at least one database alias")
	}

	executionMode := env("BACKUP_EXECUTION_MODE", "tcp")
	if executionMode != "tcp" && executionMode != "docker-compose" {
		return settings{}, errorsf("BACKUP_EXECUTION_MODE must be tcp or docker-compose")
	}
	composeDir := env("BACKUP_COMPOSE_DIRECTORY", ".")
	composeDir, err := filepath.Abs(composeDir)
	if err != nil {
		return settings{}, fmt.Errorf("resolve BACKUP_COMPOSE_DIRECTORY: %w", err)
	}
	result := settings{
		executionMode:  executionMode,
		composeFile:    env("BACKUP_COMPOSE_FILE", "docker-compose.yml"),
		composeDir:     composeDir,
		composeEnvFile: env("BACKUP_COMPOSE_ENV_FILE", ""),
		project:        project,
		directory:      env("BACKUP_DIR", "/backups"),
	}
	if result.composeEnvFile != "" && !filepath.IsAbs(result.composeEnvFile) {
		result.composeEnvFile = filepath.Join(composeDir, result.composeEnvFile)
	}
	seen := make(map[string]bool, len(aliases))
	for _, alias := range aliases {
		db, err := loadDatabase(alias, executionMode)
		if err != nil {
			return settings{}, err
		}
		if executionMode == "docker-compose" && !servicePattern.MatchString(db.service) {
			return settings{}, fmt.Errorf("%s_SERVICE must be a valid Docker Compose service name", databaseEnvPrefix(alias))
		}
		normalized := strings.ToUpper(strings.ReplaceAll(alias, "-", "_"))
		if seen[normalized] {
			return settings{}, errorsf("BACKUP_DATABASES contains duplicate aliases")
		}
		seen[normalized] = true
		result.databases = append(result.databases, db)
	}

	result.retentionCount, err = envInt("BACKUP_LOCAL_RETENTION_COUNT", 3)
	if err != nil || result.retentionCount < 1 {
		return settings{}, errorsf("BACKUP_LOCAL_RETENTION_COUNT must be an integer of at least 1")
	}
	result.interval, err = envInt("BACKUP_INTERVAL_SECONDS", 86400)
	if err != nil || result.interval < 60 {
		return settings{}, errorsf("BACKUP_INTERVAL_SECONDS must be an integer of at least 60")
	}
	result.runOnStart, err = envBool("BACKUP_RUN_ON_START", true)
	if err != nil {
		return settings{}, err
	}
	result.provider = strings.ToLower(env("BACKUP_STORAGE_PROVIDER", ""))
	result.bucket = env("BACKUP_STORAGE_BUCKET", "")
	if (result.provider == "") != (result.bucket == "") {
		return settings{}, errorsf("BACKUP_STORAGE_PROVIDER and BACKUP_STORAGE_BUCKET must be set together")
	}
	if result.provider != "" && result.provider != "s3" && result.provider != "r2" && result.provider != "gcs" {
		return settings{}, errorsf("BACKUP_STORAGE_PROVIDER must be one of: s3, r2, gcs")
	}
	result.prefix = strings.Trim(env("BACKUP_STORAGE_PREFIX", ""), "/")
	result.endpointURL = strings.TrimRight(env("R2_ENDPOINT_URL", ""), "/")
	if result.provider == "r2" && result.endpointURL == "" {
		return settings{}, errorsf("R2_ENDPOINT_URL is required when BACKUP_STORAGE_PROVIDER=r2")
	}
	return result, nil
}

func loadDatabase(alias string, executionMode string) (database, error) {
	if !aliasPattern.MatchString(alias) {
		return database{}, fmt.Errorf("invalid database alias in BACKUP_DATABASES: %s", alias)
	}
	prefix := databaseEnvPrefix(alias)
	keys := []string{"NAME", "USER"}
	if executionMode == "tcp" {
		keys = append(keys, "HOST", "PASSWORD")
	}
	missing := make([]string, 0, len(keys))
	for _, key := range keys {
		if strings.TrimSpace(os.Getenv(prefix+"_"+key)) == "" {
			missing = append(missing, prefix+"_"+key)
		}
	}
	if len(missing) > 0 {
		return database{}, fmt.Errorf("missing database configuration: %s", strings.Join(missing, ", "))
	}
	port, err := envInt(prefix+"_PORT", 5432)
	if err != nil || port < 1 || port > 65535 {
		return database{}, fmt.Errorf("%s_PORT must be between 1 and 65535", prefix)
	}
	passwordEnv := env(prefix+"_PASSWORD_ENV", "POSTGRES_PASSWORD")
	if executionMode == "docker-compose" && !envNamePattern.MatchString(passwordEnv) {
		return database{}, fmt.Errorf("%s_PASSWORD_ENV must be a valid environment variable name", prefix)
	}
	return database{
		alias: alias, service: env(prefix+"_SERVICE", ""), passwordEnv: passwordEnv, host: env(prefix+"_HOST", ""), port: port,
		name: env(prefix+"_NAME", ""), user: env(prefix+"_USER", ""),
		password: os.Getenv(prefix + "_PASSWORD"), sslMode: env(prefix+"_SSLMODE", ""),
	}, nil
}

func databaseEnvPrefix(alias string) string {
	return "BACKUP_DB_" + strings.ToUpper(strings.ReplaceAll(alias, "-", "_"))
}

func env(name, fallback string) string {
	if value, ok := os.LookupEnv(name); ok {
		return strings.TrimSpace(value)
	}
	return fallback
}

func envInt(name string, fallback int) (int, error) {
	value := env(name, strconv.Itoa(fallback))
	return strconv.Atoi(value)
}

func envBool(name string, fallback bool) (bool, error) {
	value := strings.ToLower(env(name, strconv.FormatBool(fallback)))
	switch value {
	case "true", "1", "yes":
		return true, nil
	case "false", "0", "no":
		return false, nil
	default:
		return false, fmt.Errorf("%s must be a boolean value", name)
	}
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if item := strings.TrimSpace(part); item != "" {
			result = append(result, item)
		}
	}
	return result
}

func errorsf(message string) error { return fmt.Errorf("%s", message) }

func backupPath(s settings, frequency, filename string) string {
	return filepath.Join(s.directory, frequency, filename)
}
