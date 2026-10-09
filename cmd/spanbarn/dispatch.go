package main

import (
	"errors"
	"fmt"
	"io"

	"github.com/wiebe-xyz/spanbarn/internal/config"
)

const usageText = `Usage: spanbarn [command]

With no command, or with "serve", spanbarn starts the server in the mode
set by SPANBARN_MODE.

Commands:
  serve                       start the server (default)
  version                     print version and build time
  worker-once                 run one worker pass
  user create|delete|list     manage users
  project create|list         manage projects
  apikey create|list|revoke   manage API keys
  db snapshot-settings        write a settings-only database snapshot
  help                        show this message
`

// errUnknownCommand is returned for an argument that is not a command. The
// process must exit non-zero instead of starting a second server.
var errUnknownCommand = errors.New("unknown command")

// dispatch runs the subcommand named by args[0]. handled is false when the
// caller should start the server (no args, or "serve").
func dispatch(cfg config.Config, args []string, out io.Writer) (handled bool, err error) {
	if len(args) == 0 {
		return false, nil
	}
	switch args[0] {
	case "serve":
		return false, nil
	case "help", "--help", "-h":
		_, err = io.WriteString(out, usageText)
		return true, err
	case "version", "--version", "-v":
		_, err = fmt.Fprintf(out, "spanbarn %s (built %s)\n", Version, BuildTime)
		return true, err
	case "worker-once":
		return true, runWorkerOnce(cfg)
	case "user":
		return true, runUserCmd(cfg, args[1:])
	case "project":
		return true, runProjectCmd(cfg, args[1:])
	case "apikey":
		return true, runAPIKeyCmd(cfg, args[1:])
	case "db":
		return true, runDBCmd(cfg, args[1:])
	}
	_, _ = io.WriteString(out, usageText)
	return true, fmt.Errorf("%w %q", errUnknownCommand, args[0])
}
