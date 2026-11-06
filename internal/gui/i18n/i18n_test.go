package i18n

import "testing"

func TestSystemLanguageIsOneTheWindowHas(t *testing.T) {
	got := System()
	if _, ok := Catalog(got); !ok {
		t.Fatalf("system language %q has no catalog", got)
	}
	if Resolve("ko") != "ko" || Resolve("en") != "en" {
		t.Fatal("a chosen language is not used")
	}
}
