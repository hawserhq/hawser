package main

import (
	"context"
	"errors"
	"testing"

	"github.com/wslkit/skrog/internal/wslc"
)

func names(n ...string) func(context.Context) ([]string, error) {
	return func(context.Context) ([]string, error) { return n, nil }
}

// The reclaim this backend exists to enable: nothing running means idle-stop
// may proceed.
func TestWslcBusyIdleWithNothingRunning(t *testing.T) {
	busy, err := wslcBusy(names())(context.Background())
	if err != nil || busy {
		t.Errorf("busy=%v err=%v, want not busy", busy, err)
	}
}

// The trap: a Windows-path bind leaves a skrog-share-<drive> holder running for
// as long as the bridge does. Counting it as work would veto every idle stop
// from the first bind onward, and the session would never be reclaimed.
func TestWslcBusyIgnoresShareHolders(t *testing.T) {
	for _, n := range []string{
		wslc.HolderPrefix + "c",
		"/" + wslc.HolderPrefix + "c", // the API spells names with a leading /
	} {
		busy, err := wslcBusy(names(n))(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if busy {
			t.Errorf("%q counted as work; it is Skrog's own bookkeeping", n)
		}
	}
}

func TestWslcBusySeesRealContainers(t *testing.T) {
	busy, err := wslcBusy(names("/"+wslc.HolderPrefix+"c", "/my-app"))(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !busy {
		t.Error("a real container alongside a holder must veto the idle stop")
	}
}

// An error is a veto, exactly as the distro probe treats it: stopping the
// engine kills whatever runs in it, so idling needs a definite answer.
func TestWslcBusyErrorVetoes(t *testing.T) {
	failing := func(context.Context) ([]string, error) { return nil, errors.New("unreachable") }
	if _, err := wslcBusy(failing)(context.Background()); err == nil {
		t.Error("a probe failure must surface as an error, which the supervisor treats as a veto")
	}
}

// A name that merely starts with the prefix as a substring of a longer word is
// still a holder by prefix; a user container that happens to contain the text
// elsewhere is not.
func TestWslcBusyPrefixIsAnchored(t *testing.T) {
	busy, err := wslcBusy(names("/my-" + wslc.HolderPrefix + "app"))(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !busy {
		t.Error("a user container whose name merely contains the prefix must still count as work")
	}
}
