package job

import (
	"slices"
	"testing"
	"time"
)

func TestBodyList(t *testing.T) {
	body := "intro\r\nkey: ignored on this line\r\n- a \r\n-\tb\r\n- \r\n-c\r\n- d\n"
	if got := BodyList(body, "key"); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("got %q", got)
	}
	if got := BodyList(" key:\n- a\n", "key"); got != nil {
		t.Errorf("indented key: %q", got)
	}
	if got := BodyList("key:", "key"); got != nil {
		t.Errorf("no items: %q", got)
	}
}

func TestIntersect(t *testing.T) {
	if got := Intersect([]string{"a", "b", "c"}, []string{"c", "a", "z"}); !slices.Equal(got, []string{"a", "c"}) {
		t.Errorf("got %q", got)
	}
}

func TestEnvSeconds(t *testing.T) {
	t.Setenv("X_SECONDS", "90")
	if d, err := EnvSeconds("X_SECONDS", time.Hour); d != 90*time.Second || err != nil {
		t.Errorf("got %s %v", d, err)
	}
	t.Setenv("X_SECONDS", "")
	if d, _ := EnvSeconds("X_SECONDS", time.Hour); d != time.Hour {
		t.Errorf("default: %s", d)
	}
	t.Setenv("X_SECONDS", "1h")
	if _, err := EnvSeconds("X_SECONDS", time.Hour); err == nil {
		t.Error("1h accepted")
	}
}
