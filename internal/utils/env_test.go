package utils

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestGetEnvDefault_String(t *testing.T) {
	t.Setenv("TEST_STRING", "hello")
	assert.Equal(t, "hello", GetEnvDefault("TEST_STRING", "default"))
	assert.Equal(t, "default", GetEnvDefault("TEST_STRING_UNSET", "default"))
}

func TestGetEnvDefault_Bool(t *testing.T) {
	t.Setenv("TEST_BOOL", "true")
	assert.Equal(t, true, GetEnvDefault("TEST_BOOL", false))

	t.Setenv("TEST_BOOL_INVALID", "not-a-bool")
	assert.Equal(t, false, GetEnvDefault("TEST_BOOL_INVALID", false))

	assert.Equal(t, true, GetEnvDefault("TEST_BOOL_UNSET", true))
}

func TestGetEnvDefault_Int(t *testing.T) {
	t.Setenv("TEST_INT", "42")
	assert.Equal(t, 42, GetEnvDefault("TEST_INT", 0))

	t.Setenv("TEST_INT_INVALID", "not-an-int")
	assert.Equal(t, 7, GetEnvDefault("TEST_INT_INVALID", 7))

	assert.Equal(t, 99, GetEnvDefault("TEST_INT_UNSET", 99))
}

func TestGetEnvDefault_Duration(t *testing.T) {
	t.Setenv("TEST_DURATION", "5s")
	assert.Equal(t, 5*time.Second, GetEnvDefault("TEST_DURATION", time.Second))

	t.Setenv("TEST_DURATION_INVALID", "not-a-duration")
	assert.Equal(t, time.Minute, GetEnvDefault("TEST_DURATION_INVALID", time.Minute))

	assert.Equal(t, 30*time.Second, GetEnvDefault("TEST_DURATION_UNSET", 30*time.Second))
}

func TestMustGetEnv(t *testing.T) {
	t.Setenv("TEST_MUST_STRING", "value")
	assert.Equal(t, "value", MustGetEnv("TEST_MUST_STRING"))
}

func TestMustGetEnvBool(t *testing.T) {
	t.Setenv("TEST_MUST_BOOL", "true")
	assert.Equal(t, true, MustGetEnvBool("TEST_MUST_BOOL"))
}

func TestMustGetEnvInt(t *testing.T) {
	t.Setenv("TEST_MUST_INT", "123")
	assert.Equal(t, 123, MustGetEnvInt("TEST_MUST_INT"))
}

func TestMustGetEnvDuration(t *testing.T) {
	t.Setenv("TEST_MUST_DURATION", "10s")
	assert.Equal(t, 10*time.Second, MustGetEnvDuration("TEST_MUST_DURATION"))
}
