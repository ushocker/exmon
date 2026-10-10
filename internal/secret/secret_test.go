package secret

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestNeverPrinted(t *testing.T) {
	s := Secret("hunter2")
	type holder struct{ Pass Secret }
	h := holder{s}
	outputs := []string{
		fmt.Sprint(s), fmt.Sprintf("%s %v %q %x %#v %+v", s, s, s, s, s, h),
		fmt.Errorf("token failed for %v", s).Error(),
		errors.New(fmt.Sprint(h)).Error(),
	}
	b, _ := json.Marshal(h)
	outputs = append(outputs, string(b))
	for _, o := range outputs {
		if strings.Contains(o, "hunter2") {
			t.Errorf("secret leaked: %s", o)
		}
	}
	if s.Reveal() != "hunter2" {
		t.Error("Reveal must return the value")
	}
}

func TestUnmarshal(t *testing.T) {
	var v struct{ P Secret }
	if err := json.Unmarshal([]byte(`{"P":"abc"}`), &v); err != nil || v.P.Reveal() != "abc" || !v.P.IsSet() {
		t.Errorf("got %v %v", v.P.Reveal(), err)
	}
}
