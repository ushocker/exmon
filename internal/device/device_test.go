package device

import (
	"strings"
	"testing"
)

func TestGet(t *testing.T) {
	i := Get(false)
	if i.Computer == "" || !strings.Contains(i.OS, "build") {
		t.Errorf("incomplete device info: %+v", i)
	}
	t.Log(i)
}
