package logx

import (
	"log"
	"sync/atomic"

	"simple-nat-traversal/internal/config"
)

type level int32

const (
	levelDebug level = iota
	levelInfo
	levelWarn
	levelError
)

var currentLevel atomic.Int32

func init() {
	currentLevel.Store(int32(levelInfo))
}

func SetLevel(raw string) (string, error) {
	normalized, err := config.NormalizeLogLevel(raw)
	if err != nil {
		return "", err
	}
	currentLevel.Store(int32(parseLevel(normalized)))
	return normalized, nil
}

func CurrentLevel() string {
	switch level(currentLevel.Load()) {
	case levelDebug:
		return config.LogLevelDebug
	case levelWarn:
		return config.LogLevelWarn
	case levelError:
		return config.LogLevelError
	default:
		return config.LogLevelInfo
	}
}

func Debugf(format string, args ...any) {
	logf(levelDebug, "DEBUG", format, args...)
}

func Infof(format string, args ...any) {
	logf(levelInfo, "INFO", format, args...)
}

func Warnf(format string, args ...any) {
	logf(levelWarn, "WARN", format, args...)
}

func Errorf(format string, args ...any) {
	logf(levelError, "ERROR", format, args...)
}

func logf(target level, label, format string, args ...any) {
	if target < level(currentLevel.Load()) {
		return
	}
	log.Printf("["+label+"] "+format, args...)
}

func parseLevel(raw string) level {
	switch raw {
	case config.LogLevelDebug:
		return levelDebug
	case config.LogLevelWarn:
		return levelWarn
	case config.LogLevelError:
		return levelError
	default:
		return levelInfo
	}
}
