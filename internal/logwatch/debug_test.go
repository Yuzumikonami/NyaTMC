package logwatch

import (
	"regexp"
	"testing"
)

func TestDebugRegex(t *testing.T) {
	line := `[12:01:00] [Server thread/INFO]: Steve joined the game`
	body := stripTimestamp(line)
	t.Logf("body=%q", body)
	t.Logf("reJoined=%v", reJoined.MatchString(body))
	t.Logf("reJoinedRaw=%v", reJoined.MatchString(line))
	t.Logf("simple=%v", regexp.MustCompile(`^(\S+) joined the game$`).MatchString(body))
	t.Logf("class=%v", regexp.MustCompile(`[^\s\[]+`).MatchString(body))
}
