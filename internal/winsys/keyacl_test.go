package winsys

import "testing"

func TestGrantKeyRead(t *testing.T) {
	name := testKeyName(t)
	s, err := CreateKey(ProviderSoftware, name, ECDSAP256, false, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { DeleteKey(ProviderSoftware, name, false) })
	defer s.Close()

	const sid = "S-1-5-80-1111-2222-3333-4444-5555" // shaped like an NT SERVICE\<name> SID
	before, err := s.KeyDACL()
	if err != nil {
		t.Skipf("this provider doesn't expose key permissions: %v", err)
	}
	changed, err := s.GrantKeyRead(sid)
	if err != nil || !changed {
		t.Fatalf("grant: changed=%v err=%v (DACL was %s)", changed, err, before)
	}
	if ok, _ := s.HasKeyAccess(sid); !ok {
		after, _ := s.KeyDACL()
		t.Errorf("grant not visible: %s", after)
	}
	if changed, err := s.GrantKeyRead(sid); changed || err != nil {
		t.Errorf("second grant should change nothing: %v %v", changed, err)
	}
}

func TestAddAllowACE(t *testing.T) {
	cases := map[string]string{
		"D:(A;;GA;;;SY)":            "D:(A;;GA;;;SY)(A;;GR;;;X)",
		"D:P(A;;GA;;;SY)S:(AU;;;;)": "D:P(A;;GA;;;SY)(A;;GR;;;X)S:(AU;;;;)",
		"":                          "D:(A;;GR;;;X)",
	}
	for in, want := range cases {
		if got := addAllowACE(in, "(A;;GR;;;X)"); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}
