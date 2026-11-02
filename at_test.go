package main

import (
	"reflect"
	"testing"
)

func TestSplitFields(t *testing.T) {
	got := splitFields(`0,0,"Vodafone, UA",7`)
	want := []string{"0", "0", "Vodafone, UA", "7"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestFindLineTakesLast(t *testing.T) {
	v, ok := findLine([]string{"+CEREG: 1", "+CEREG: 2,1,\"1F4A\",\"0A3F2C11\",7"}, "+CEREG:")
	if !ok || v != `2,1,"1F4A","0A3F2C11",7` {
		t.Fatalf("got %q %v", v, ok)
	}
}

func TestTechAndReg(t *testing.T) {
	cases := map[int]string{0: "2G", 2: "3G", 7: "LTE", 13: "5G", -1: ""}
	for act, want := range cases {
		if got := actToTech(act); got != want {
			t.Errorf("actToTech(%d) = %q, want %q", act, got, want)
		}
	}
	if regToStr(5) != "roaming" || regToStr(1) != "home" || regToStr(-1) != "unknown" {
		t.Error("regToStr")
	}
}

func TestCopsList(t *testing.T) {
	line := `+COPS: (2,"Kyivstar","KS","25503",7),(3,"lifecell","life:)","25506",2),,(0-4),(0-2)`
	m := copsTuple.FindAllStringSubmatch(line, -1)
	if len(m) != 2 || m[0][2] != "Kyivstar" || m[1][1] != "3" || m[1][5] != "2" {
		t.Fatalf("parsed %q", m)
	}
}

func TestTrimAT(t *testing.T) {
	if c, ok := trimAT(" at+csq\r\n"); !ok || c != "at+csq" {
		t.Fatalf("got %q %v", c, ok)
	}
	if _, ok := trimAT("rm -rf /"); ok {
		t.Fatal("non-AT command accepted")
	}
}
