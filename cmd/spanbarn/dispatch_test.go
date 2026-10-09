package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/wiebe-xyz/spanbarn/internal/config"
)

func TestDispatchServeCases(t *testing.T) {
	for _, args := range [][]string{nil, {}, {"serve"}} {
		var out bytes.Buffer
		handled, err := dispatch(config.Config{}, args, &out)
		if handled || err != nil {
			t.Errorf("args %v: handled=%v err=%v, want server start", args, handled, err)
		}
	}
}

func TestDispatchHelp(t *testing.T) {
	for _, arg := range []string{"help", "--help", "-h"} {
		var out bytes.Buffer
		handled, err := dispatch(config.Config{}, []string{arg}, &out)
		if !handled || err != nil {
			t.Errorf("%s: handled=%v err=%v, want handled and nil", arg, handled, err)
		}
		if !strings.Contains(out.String(), "Usage: spanbarn") {
			t.Errorf("%s: no usage in output %q", arg, out.String())
		}
	}
}

func TestDispatchUnknownNeverStartsServer(t *testing.T) {
	for _, arg := range []string{"--bogus", "-x", "servee", "start"} {
		var out bytes.Buffer
		handled, err := dispatch(config.Config{}, []string{arg}, &out)
		if !handled {
			t.Errorf("%s: would start the server", arg)
		}
		if !errors.Is(err, errUnknownCommand) {
			t.Errorf("%s: err=%v, want errUnknownCommand", arg, err)
		}
		if !strings.Contains(out.String(), "Usage: spanbarn") {
			t.Errorf("%s: no usage printed", arg)
		}
	}
}

func TestDispatchVersion(t *testing.T) {
	var out bytes.Buffer
	handled, err := dispatch(config.Config{}, []string{"--version"}, &out)
	if !handled || err != nil || !strings.Contains(out.String(), "spanbarn dev") {
		t.Errorf("handled=%v err=%v out=%q", handled, err, out.String())
	}
}
