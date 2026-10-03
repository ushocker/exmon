package limits

import "testing"

func TestRangeAndCount(t *testing.T) {
	if Range("x", 5, 1, 50) != nil || Range("x", 1, 1, 50) != nil || Range("x", 50, 1, 50) != nil {
		t.Error("values inside the range (including the ends) must pass")
	}
	if err := Range("log.maxFiles", 5000, 1, 50); err == nil || err.Error() != "log.maxFiles 5000 is out of range 1-50" {
		t.Errorf("got %v", err)
	}
	if Range("x", 0, 1, 50) == nil {
		t.Error("below the range must fail")
	}
	if Count("probe.endpoints", 25, 25) != nil || Count("probe.endpoints", 26, 25) == nil {
		t.Error("count limit wrong")
	}
	if First(nil, Range("a", 9, 1, 2), Range("b", 9, 1, 2)).Error() != "a 9 is out of range 1-2" {
		t.Error("First should return the first error")
	}
}
