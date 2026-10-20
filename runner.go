package main

import (
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type backupFile struct {
	database database
	path     string
	name     string
}

func createBackup(s settings, frequency string) (string, error) {
	if !frequencyPattern.MatchString(frequency) {
		return "", fmt.Errorf("frequency must contain only letters, numbers, underscores, or hyphens")
	}
	timestamp := time.Now().UTC().Format("20060102T150405Z")
	files := make([]backupFile, 0, len(s.databases))
	for _, db := range s.databases {
		name := fmt.Sprintf("%s-%s-%s.dump", s.project, db.alias, timestamp)
		path := backupPath(s, frequency, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return "", fmt.Errorf("create backup directory: %w", err)
		}
		temporary := path + ".tmp"
		_ = os.Remove(temporary)
		if err := dumpDatabase(s, db, temporary); err != nil {
			_ = os.Remove(temporary)
			return "", fmt.Errorf("dump database %s: %w", db.alias, err)
		}
		if err := os.Rename(temporary, path); err != nil {
			_ = os.Remove(temporary)
			return "", fmt.Errorf("finalize backup for database %s: %w", db.alias, err)
		}
		log.Printf("Created backup for database %s: %s", db.alias, path)
		files = append(files, backupFile{database: db, path: path, name: name})
	}

	store, err := newObjectStore(s)
	if err != nil {
		return "", err
	}
	for _, file := range files {
		key := filepath.ToSlash(filepath.Join(frequency, file.name))
		if err := store.upload(file.path, key); err != nil {
			return "", fmt.Errorf("upload backup %s: %w", file.name, err)
		}
		if s.provider != "" {
			log.Printf("Uploaded backup: %s", key)
		}
	}
	for _, db := range s.databases {
		if err := pruneLocal(s, db, frequency); err != nil {
			return "", fmt.Errorf("prune local backups for %s: %w", db.alias, err)
		}
	}
	return timestamp, nil
}

func restoreBackup(s settings, timestamp, backupName, frequency string, aliases []string, latest bool) error {
	if !latest && backupName == "" && !timestampPattern.MatchString(timestamp) {
		return fmt.Errorf("timestamp must use UTC format YYYYMMDDTHHMMSSZ")
	}
	if !frequencyPattern.MatchString(frequency) {
		return fmt.Errorf("invalid backup frequency")
	}
	selected, err := selectDatabases(s, aliases)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.directory, 0o750); err != nil {
		return fmt.Errorf("create backup directory: %w", err)
	}
	store, err := newObjectStore(s)
	if err != nil {
		return err
	}
	if latest {
		timestamp, err = latestBackupTimestamp(s, store, selected, frequency)
		if err != nil {
			return err
		}
		log.Printf("Selected latest complete backup set: %s (frequency %s)", timestamp, frequency)
	} else if backupName != "" {
		timestamp, err = timestampFromBackupName(s.project, selected, backupName)
		if err != nil {
			return err
		}
		log.Printf("Selected backup set from filename %s: %s", backupName, timestamp)
	}
	files := make([]backupFile, 0, len(selected))
	for _, db := range selected {
		name := fmt.Sprintf("%s-%s-%s.dump", s.project, db.alias, timestamp)
		path := backupPath(s, frequency, name)
		if _, err := os.Stat(path); err != nil {
			if !errorsIsNotExist(err) {
				return fmt.Errorf("inspect backup for database %s: %w", db.alias, err)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
				return fmt.Errorf("create backup directory: %w", err)
			}
			key := filepath.ToSlash(filepath.Join(frequency, name))
			temporary := path + ".tmp"
			found, err := store.download(key, temporary)
			if err != nil {
				_ = os.Remove(temporary)
				return fmt.Errorf("download backup for database %s: %w", db.alias, err)
			}
			if !found {
				_ = os.Remove(temporary)
				return fmt.Errorf("backup not found for database %s: %s", db.alias, name)
			}
			if err := os.Rename(temporary, path); err != nil {
				_ = os.Remove(temporary)
				return fmt.Errorf("finalize downloaded backup %s: %w", name, err)
			}
		}
		files = append(files, backupFile{database: db, path: path, name: name})
	}
	for _, file := range files {
		if err := restoreDatabase(s, file.database, file.path); err != nil {
			return fmt.Errorf("restore database %s: %w", file.database.alias, err)
		}
		log.Printf("Restored database %s from %s", file.database.alias, file.name)
	}
	return nil
}

func timestampFromBackupName(project string, databases []database, name string) (string, error) {
	if name == "" || strings.ContainsAny(name, `/\\`) || filepath.Base(name) != name || !strings.HasSuffix(name, ".dump") {
		return "", fmt.Errorf("backup name must be a dump filename without a path")
	}
	stem := strings.TrimSuffix(name, ".dump")
	for _, db := range databases {
		prefix := fmt.Sprintf("%s-%s-", project, db.alias)
		if !strings.HasPrefix(stem, prefix) {
			continue
		}
		timestamp := strings.TrimPrefix(stem, prefix)
		if timestampPattern.MatchString(timestamp) {
			return timestamp, nil
		}
	}
	return "", fmt.Errorf("backup name must match <project>-<selected-database>-<YYYYMMDDTHHMMSSZ>.dump")
}

func latestBackupTimestamp(s settings, store *objectStore, databases []database, frequency string) (string, error) {
	available := make(map[string]map[string]bool)
	add := func(alias, timestamp string) {
		if available[timestamp] == nil {
			available[timestamp] = make(map[string]bool)
		}
		available[timestamp][alias] = true
	}

	for _, db := range databases {
		prefix := fmt.Sprintf("%s-%s-", s.project, db.alias)
		directory := filepath.Join(s.directory, frequency)
		entries, err := os.ReadDir(directory)
		if err != nil && !errorsIsNotExist(err) {
			return "", fmt.Errorf("list local backups for %s: %w", db.alias, err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) || !strings.HasSuffix(entry.Name(), ".dump") {
				continue
			}
			timestamp := strings.TrimSuffix(strings.TrimPrefix(entry.Name(), prefix), ".dump")
			if timestampPattern.MatchString(timestamp) {
				add(db.alias, timestamp)
			}
		}
	}

	if s.provider != "" {
		for _, db := range databases {
			prefix := fmt.Sprintf("%s-%s-", s.project, db.alias)
			keys, err := store.list(filepath.ToSlash(filepath.Join(frequency, prefix)))
			if err != nil {
				return "", fmt.Errorf("list remote backups for %s: %w", db.alias, err)
			}
			for _, key := range keys {
				name := filepath.Base(key)
				if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".dump") {
					continue
				}
				timestamp := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".dump")
				if timestampPattern.MatchString(timestamp) {
					add(db.alias, timestamp)
				}
			}
		}
	}

	timestamps := make([]string, 0, len(available))
	for timestamp := range available {
		timestamps = append(timestamps, timestamp)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(timestamps)))
	for _, timestamp := range timestamps {
		complete := true
		for _, db := range databases {
			if !available[timestamp][db.alias] {
				complete = false
				break
			}
		}
		if complete {
			return timestamp, nil
		}
	}
	return "", fmt.Errorf("no complete backup set found for frequency %s", frequency)
}

func schedule(s settings, frequency string) error {
	if s.runOnStart {
		scheduledBackup(s, frequency)
	}
	log.Printf("Backup scheduler active; interval is %d seconds.", s.interval)
	for {
		time.Sleep(time.Duration(s.interval) * time.Second)
		scheduledBackup(s, frequency)
	}
}

func scheduledBackup(s settings, frequency string) {
	timestamp, err := createBackup(s, frequency)
	if err != nil {
		log.Printf("Scheduled backup failed; the next run will retry: %v", err)
		return
	}
	log.Printf("Backup set completed: %s", timestamp)
}

func dumpDatabase(s settings, db database, destination string) error {
	args := postgresConnectionArgs(s, db)
	args = append(args, "--username", db.user, "--format=custom", "--no-password")
	var output io.Writer
	if s.executionMode == "docker-compose" {
		file, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		output = file
		args = append(args, db.name)
		commandErr := runPostgresCommand(s, db, "pg_dump", nil, output, args...)
		closeErr := file.Close()
		if commandErr != nil {
			return commandErr
		}
		return closeErr
	}
	args = append(args, "--file", destination, db.name)
	return runPostgresCommand(s, db, "pg_dump", nil, output, args...)
}

func restoreDatabase(s settings, db database, source string) error {
	connection := postgresConnectionArgs(s, db)
	connection = append(connection, "--username", db.user)
	if err := runPostgresCommand(s, db, "dropdb", nil, nil, append(connection, "--if-exists", "--force", db.name)...); err != nil {
		return fmt.Errorf("drop database: %w", err)
	}
	if err := runPostgresCommand(s, db, "createdb", nil, nil, append(connection, "--owner", db.user, db.name)...); err != nil {
		return fmt.Errorf("create database: %w", err)
	}
	restoreArgs := append(connection, "--dbname", db.name, "--no-owner", "--no-acl", "--exit-on-error")
	var input io.Reader
	if s.executionMode == "docker-compose" {
		file, err := os.Open(source)
		if err != nil {
			return fmt.Errorf("open dump: %w", err)
		}
		defer file.Close()
		input = file
	} else {
		restoreArgs = append(restoreArgs, source)
	}
	if err := runPostgresCommand(s, db, "pg_restore", input, nil, restoreArgs...); err != nil {
		return fmt.Errorf("load dump: %w", err)
	}
	return nil
}

func postgresConnectionArgs(s settings, db database) []string {
	if s.executionMode == "docker-compose" {
		return nil
	}
	return []string{"--host", db.host, "--port", fmt.Sprint(db.port)}
}

func runPostgresCommand(s settings, db database, name string, stdin io.Reader, stdout io.Writer, args ...string) error {
	commandName := name
	commandArgs := args
	if s.executionMode == "docker-compose" {
		commandName = "docker"
		composeArgs := []string{"compose", "--project-directory", s.composeDir, "--file", s.composeFile}
		if s.composeEnvFile != "" {
			composeArgs = append(composeArgs, "--env-file", s.composeEnvFile)
		}
		composeArgs = append(composeArgs,
			"exec", "--no-TTY", db.service, "sh", "-c",
			`PGPASSWORD="$(printenv "$1")"; export PGPASSWORD; shift; exec "$@"`,
			"backup-sys", db.passwordEnv, name,
		)
		commandArgs = append(composeArgs, args...)
	}
	command := exec.Command(commandName, commandArgs...)
	if s.executionMode == "docker-compose" {
		command.Dir = s.composeDir
	}
	command.Env = os.Environ()
	if s.executionMode == "tcp" {
		command.Env = append(command.Env, "PGPASSWORD="+db.password)
	}
	if s.executionMode == "tcp" && db.sslMode != "" {
		command.Env = append(command.Env, "PGSSLMODE="+db.sslMode)
	}
	command.Stdin = stdin
	if stdout == nil {
		stdout = os.Stdout
	}
	command.Stdout = stdout
	command.Stderr = os.Stderr
	return command.Run()
}

func pruneLocal(s settings, db database, frequency string) error {
	directory := filepath.Join(s.directory, frequency)
	entries, err := os.ReadDir(directory)
	if err != nil {
		if errorsIsNotExist(err) {
			return nil
		}
		return err
	}
	prefix := fmt.Sprintf("%s-%s-", s.project, db.alias)
	type datedFile struct {
		path    string
		modTime time.Time
	}
	files := make([]datedFile, 0)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) || !strings.HasSuffix(entry.Name(), ".dump") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		files = append(files, datedFile{path: filepath.Join(directory, entry.Name()), modTime: info.ModTime()})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].modTime.After(files[j].modTime) })
	for _, stale := range files[min(s.retentionCount, len(files)):] {
		if err := os.Remove(stale.path); err != nil {
			return err
		}
		log.Printf("Removed old local backup: %s", filepath.Base(stale.path))
	}
	return nil
}

func selectDatabases(s settings, aliases []string) ([]database, error) {
	if len(aliases) == 0 {
		return s.databases, nil
	}
	lookup := make(map[string]database, len(s.databases))
	for _, db := range s.databases {
		lookup[db.alias] = db
	}
	selected := make([]database, 0, len(aliases))
	for _, alias := range aliases {
		db, ok := lookup[alias]
		if !ok {
			return nil, fmt.Errorf("unknown database alias: %s", alias)
		}
		selected = append(selected, db)
	}
	return selected, nil
}

func errorsIsNotExist(err error) bool { return err == fs.ErrNotExist || os.IsNotExist(err) }
