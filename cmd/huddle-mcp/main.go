// Command huddle-mcp is the client side of huddle: the hooks the Huddle
// plugin runs, the bridge for Claude Code channels and the image upload. It
// talks to a huddle server over HTTP and never touches its database, so this
// repository holds no server code.
//
//	huddle-mcp rewake       (asyncRewake hook: waits for a doorbell, exits 2 to wake Claude)
//	huddle-mcp inbox-hook   (UserPromptSubmit hook: new events into the prompt)
//	huddle-mcp memory-hook  (Stop hook: keeps the session's memory fresh)
//	huddle-mcp channel      (stdio bridge for Claude Code channels)
//	huddle-mcp save-token -url <server> -group <group>  (token on stdin; from the hook_token tool)
//	huddle-mcp attach -url <upload_url> -ticket <ticket> skiss.png ...  (uploads images; ticket from the upload_image tool)
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/dimelords/huddle/internal/channel"
)

var version = "dev"

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch os.Args[1] {
	case "rewake":
		os.Exit(rewake(ctx, log))
	case "inbox-hook":
		inboxHook(ctx, os.Stdout)
		return
	case "memory-hook":
		memoryHook(os.Stdin, os.Stdout, stateDir())
		return
	case "version", "-version", "--version":
		fmt.Println(version)
		return
	}
	if err := run(ctx, log, os.Args[1], os.Args[2:]); err != nil {
		log.Error("fatal", "cmd", os.Args[1], "err", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: huddle-mcp rewake | inbox-hook | memory-hook | channel | save-token | attach | version  (-h for flags)")
}

func run(ctx context.Context, log *slog.Logger, cmd string, args []string) error {
	switch cmd {
	case "channel":
		// Uses the same tokens as the hooks: HUDDLE_TOKEN, or those saved
		// with save-token.
		ts := hookTokens()
		if len(ts) == 0 {
			return errors.New("no token: set HUDDLE_TOKEN and HUDDLE_URL, or save a hook token with save-token")
		}
		srcs := make([]channel.Source, len(ts))
		for i, t := range ts {
			srcs[i] = channel.Source{BaseURL: t.URL, Token: t.Token}
		}
		return channel.Run(ctx, channel.Config{Sources: srcs, Version: version, Log: log}, os.Stdin, os.Stdout)

	case "attach":
		fs := flag.NewFlagSet(cmd, flag.ExitOnError)
		url := fs.String("url", "", "upload_url from the upload_image tool")
		ticket := fs.String("ticket", os.Getenv("HUDDLE_UPLOAD_TICKET"), "upload ticket from the upload_image tool (or HUDDLE_UPLOAD_TICKET)")
		_ = fs.Parse(args)
		if *url == "" || *ticket == "" || fs.NArg() == 0 {
			return errors.New("usage: huddle-mcp attach -url <upload_url> -ticket <ticket> <image> [image...]")
		}
		return attach(ctx, *url, *ticket, fs.Args(), os.Stdout)

	case "save-token":
		fs := flag.NewFlagSet(cmd, flag.ExitOnError)
		url := fs.String("url", "", "the huddle server (HUDDLE_URL from the hook_token tool)")
		group := fs.String("group", "", "the group the token is locked to")
		remove := fs.Bool("remove", false, "forget the token for this server and group")
		_ = fs.Parse(args)
		if *url == "" {
			return errors.New("usage: huddle-mcp save-token -url <server> -group <group>  (token on stdin)")
		}
		token := ""
		if !*remove {
			var err error
			if token, err = readToken(os.Stdin); err != nil {
				return err
			}
			if !strings.HasPrefix(token, "hdl_hk_") {
				return errors.New("expected a hook token (hdl_hk_...) on stdin; get one with the hook_token tool")
			}
		}
		if err := saveToken(*url, *group, token); err != nil {
			return err
		}
		if *remove {
			fmt.Println("hook token removed")
		} else {
			fmt.Println("hook token saved: the Huddle hooks use it from the next turn")
		}
		return nil

	default:
		usage()
		return fmt.Errorf("unknown command %q", cmd)
	}
}

// env is the value of k, or def when it is unset or empty.
func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
