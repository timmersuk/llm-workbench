package utils

import (
	"os"
	"strconv"
	"time"

	"github.com/sirupsen/logrus"
)

// GetEnvDefault reads the environment variable named key and parses it into the
// same type as defaultValue. If the variable is unset, or set but not parseable
// as T, defaultValue is returned.
func GetEnvDefault[T any](key string, defaultValue T) T {
	raw, ok := os.LookupEnv(key)
	if !ok {
		return defaultValue
	}

	switch any(defaultValue).(type) {
	case string:
		return any(raw).(T)
	case bool:
		v, err := strconv.ParseBool(raw)
		if err != nil {
			return defaultValue
		}
		return any(v).(T)
	case int:
		v, err := strconv.Atoi(raw)
		if err != nil {
			return defaultValue
		}
		return any(v).(T)
	case time.Duration:
		v, err := time.ParseDuration(raw)
		if err != nil {
			return defaultValue
		}
		return any(v).(T)
	default:
		return defaultValue
	}
}

// MustGetEnv returns the environment variable named key, or calls logrus.Fatal
// if it is unset.
func MustGetEnv(key string) string {
	raw, ok := os.LookupEnv(key)
	if !ok {
		logrus.Fatalf("required environment variable %s is not set", key)
	}
	return raw
}

// MustGetEnvBool returns the environment variable named key parsed as a bool,
// or calls logrus.Fatal if it is unset or not a valid bool.
func MustGetEnvBool(key string) bool {
	raw := MustGetEnv(key)
	v, err := strconv.ParseBool(raw)
	if err != nil {
		logrus.Fatalf("environment variable %s is not a valid bool: %v", key, err)
	}
	return v
}

// MustGetEnvInt returns the environment variable named key parsed as an int,
// or calls logrus.Fatal if it is unset or not a valid int.
func MustGetEnvInt(key string) int {
	raw := MustGetEnv(key)
	v, err := strconv.Atoi(raw)
	if err != nil {
		logrus.Fatalf("environment variable %s is not a valid int: %v", key, err)
	}
	return v
}

// MustGetEnvDuration returns the environment variable named key parsed as a
// time.Duration, or calls logrus.Fatal if it is unset or not a valid duration.
func MustGetEnvDuration(key string) time.Duration {
	raw := MustGetEnv(key)
	v, err := time.ParseDuration(raw)
	if err != nil {
		logrus.Fatalf("environment variable %s is not a valid duration: %v", key, err)
	}
	return v
}
