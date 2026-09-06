package main

import (
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/e-aleixandre/moa/pkg/core"
)

func runHooks(args []string) {
	if err := hooksMain(args); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func hooksMain(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: moa hooks add|list|rm")
	}
	switch args[0] {
	case "add":
		return hooksAdd(args[1:])
	case "list":
		return hooksList(args[1:])
	case "rm", "remove":
		return hooksRm(args[1:])
	default:
		return fmt.Errorf("unknown hooks command %q (want add, list, rm)", args[0])
	}
}

func hooksAdd(args []string) error {
	fs := flag.NewFlagSet("hooks add", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	project := fs.String("project", "", "Project directory (target {project: DIR})")
	sessionID := fs.String("session", "", "Deliver to this session id (target {session: ID})")
	inbox := fs.Bool("inbox", false, "Leave events in the inbox")
	whenNone := fs.String("when-none", "inbox", "When the project has no live session: inbox or create")
	whenMany := fs.String("when-many", "inbox", "When the project has several live sessions: inbox or latest")
	model := fs.String("model", "", "Model for when-none=create (and New session)")
	thinking := fs.String("thinking", "", "Thinking level for when-none=create")
	yolo := fs.Bool("yolo", false, "Create sessions in yolo permission mode")
	autorun := fs.Bool("autorun", false, "Start a turn on delivery to an idle session")
	name, flagArgs := splitSourceAndFlags(args)
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}
	if name == "" {
		if fs.NArg() != 1 {
			return fmt.Errorf("usage: moa hooks add <source> --project DIR [--session ID | --inbox] [flags]")
		}
		name = fs.Arg(0)
	} else if fs.NArg() != 0 {
		return fmt.Errorf("usage: moa hooks add <source> --project DIR [--session ID | --inbox] [flags]")
	}
	if err := core.ValidateEventSourceName(name); err != nil {
		return err
	}

	n := 0
	if *project != "" {
		n++
	}
	if *sessionID != "" {
		n++
	}
	if *inbox {
		n++
	}
	if n == 0 {
		return fmt.Errorf("one of --project, --session or --inbox is required")
	}
	if n > 1 {
		return fmt.Errorf("--project, --session and --inbox are mutually exclusive")
	}

	src := core.EventSourceConfig{
		WhenNone: *whenNone,
		WhenMany: *whenMany,
		Create: core.EventCreateConfig{
			Model:    *model,
			Thinking: *thinking,
			Yolo:     *yolo,
		},
	}
	if *autorun {
		on := true
		src.Autorun = &on
	}
	switch {
	case *inbox:
		src.Target = core.EventTarget{Kind: core.EventTargetInbox}
	case *sessionID != "":
		src.Target = core.EventTarget{Kind: core.EventTargetSession, Session: *sessionID}
	default:
		abs, err := filepath.Abs(*project)
		if err != nil {
			return fmt.Errorf("project: %w", err)
		}
		src.Target = core.EventTarget{Kind: core.EventTargetProject, Project: abs}
	}

	secret, err := newHookSecret()
	if err != nil {
		return err
	}
	src.Secret = secret
	if err := src.Validate(name); err != nil {
		return err
	}

	var existed bool
	if err := core.SaveGlobalConfig(func(cfg *core.MoaConfig) {
		if cfg.Events == nil {
			cfg.Events = &core.EventsConfig{Sources: map[string]core.EventSourceConfig{}}
		}
		if cfg.Events.Sources == nil {
			cfg.Events.Sources = map[string]core.EventSourceConfig{}
		}
		_, existed = cfg.Events.Sources[name]
		cfg.Events.Sources[name] = src
	}); err != nil {
		return err
	}
	if existed {
		fmt.Printf("updated source %s\n", name)
	} else {
		fmt.Printf("added source %s\n", name)
	}
	fmt.Printf("Hook URL path (contains the secret; store it in the provider now):\n/hooks/%s/%s\n", name, secret)
	return nil
}

func hooksList(args []string) error {
	fs := flag.NewFlagSet("hooks list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	showSecrets := fs.Bool("show-secrets", false, "Print the full hook path including the secret")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg := core.LoadGlobalConfig()
	if cfg.Events == nil || len(cfg.Events.Sources) == 0 {
		fmt.Println("no event sources")
		return nil
	}
	names := make([]string, 0, len(cfg.Events.Sources))
	for name := range cfg.Events.Sources {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		src := cfg.Events.Sources[name]
		path := "/hooks/" + name + "/"
		if *showSecrets {
			path += src.Secret
		} else {
			path += "********"
		}
		fmt.Printf("%s\t%s\t%s\n", name, describeHookTarget(src), path)
	}
	return nil
}

func hooksRm(args []string) error {
	fs := flag.NewFlagSet("hooks rm", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: moa hooks rm <source>")
	}
	name := fs.Arg(0)
	var found bool
	if err := core.SaveGlobalConfig(func(cfg *core.MoaConfig) {
		if cfg.Events == nil || cfg.Events.Sources == nil {
			return
		}
		if _, ok := cfg.Events.Sources[name]; ok {
			found = true
			delete(cfg.Events.Sources, name)
		}
	}); err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("source %q not found", name)
	}
	fmt.Printf("removed source %s\n", name)
	return nil
}

func splitSourceAndFlags(args []string) (name string, flags []string) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return args[0], args[1:]
	}
	return "", args
}

func describeHookTarget(src core.EventSourceConfig) string {
	switch src.TargetKind() {
	case core.EventTargetProject:
		return "project:" + src.Target.Project
	case core.EventTargetSession:
		return "session:" + src.Target.Session
	default:
		return "inbox"
	}
}

func newHookSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
