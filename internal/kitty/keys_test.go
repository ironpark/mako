package kitty

import (
	"fmt"
	"testing"

	"github.com/ironpark/gostty"
)

// Every combination of flags a program can push reads back as itself.
func TestFlagsReadBackAsPushed(t *testing.T) {
	for pushed := KeyFlags(0); pushed < 32; pushed++ {
		vt, err := gostty.New(20, 5)
		if err != nil {
			t.Fatal(err)
		}
		if pushed != 0 {
			fmt.Fprintf(vt, "\x1b[>%du", pushed)
		}
		want := pushed
		got := Flags(vt.Terminal())
		vt.Close()
		if want != 0 {
			want |= Disambiguate // which ghostty does under any flags
		}
		if got != want {
			t.Errorf("pushed %05b, read %05b", want, got)
		}
	}
}

func TestRestoreSetsTheFlags(t *testing.T) {
	vt, _ := gostty.New(20, 5)
	defer vt.Close()
	flags := Disambiguate | EventTypes | AllKeysAsCodes
	_, _ = vt.Write([]byte(flags.Restore()))
	if got := Flags(vt.Terminal()); got != flags {
		t.Fatalf("restored flags read %05b, want %05b", got, flags)
	}
	if KeyFlags(0).Restore() != "" {
		t.Fatal("no flags restore as something")
	}
}
