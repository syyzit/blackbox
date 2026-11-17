package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/pretty"
	"github.com/tidwall/sjson"
)

// hookEvents are the Claude Code events the adapter registers for.
var hookEvents = []string{"PreToolUse", "PostToolUse", "PostToolUseFailure"}

// hookTimeoutSeconds bounds one hook run. The hook does a policy lookup and a SQLite
// write, so this is generous; Claude Code's own default is minutes.
const hookTimeoutSeconds = 10

// cmdHookInstall adds the adapter to a Claude Code settings file, or removes it.
func cmdHookInstall(args []string) int {
	fs := flag.NewFlagSet("hook install", flag.ContinueOnError)
	scope := fs.String("scope", "local", "settings file: local (.claude/settings.local.json), project (.claude/settings.json) or user (~/.claude/settings.json)")
	dir := fs.String("dir", "", "project directory (default: working directory)")
	command := fs.String("command", "", "blackbox binary the hook runs (default: this binary)")
	home := fs.String("home", "", "data directory the hook records to (default: ~/.blackbox)")
	mcp := fs.Bool("mcp", false, "also record and check MCP tools")
	remove := fs.Bool("remove", false, "remove the hook instead of adding it")
	dryRun := fs.Bool("dry-run", false, "print the result without writing")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	path, err := hookSettingsPath(*scope, *dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "blackbox hook install:", err)
		return 2
	}
	bin := *command
	if bin == "" {
		if bin, err = os.Executable(); err != nil {
			fmt.Fprintln(os.Stderr, "blackbox hook install:", err)
			return 1
		}
	}
	hookArgs := []string{"hook"}
	if *home != "" {
		hookArgs = append(hookArgs, "-home", *home)
	}
	if *mcp {
		hookArgs = append(hookArgs, "-mcp")
	}

	original, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(os.Stderr, "blackbox hook install:", err)
		return 1
	}
	existed := err == nil

	updated, err := editHookSettings(original, bin, hookArgs, *remove)
	if err != nil {
		fmt.Fprintf(os.Stderr, "blackbox hook install: %s: %v\n", path, err)
		return 1
	}
	if string(updated) == string(original) {
		fmt.Printf("%s already up to date\n", path)
		return 0
	}
	if *dryRun {
		fmt.Printf("dry run: %s would become:\n%s\n", path, updated)
		return 0
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "blackbox hook install:", err)
		return 1
	}
	mode := os.FileMode(0o644)
	if existed {
		if info, err := os.Stat(path); err == nil {
			mode = info.Mode().Perm()
		}
		backup := path + ".blackbox-backup-" + time.Now().Format("20060102-150405")
		if err := os.WriteFile(backup, original, mode); err != nil {
			fmt.Fprintf(os.Stderr, "blackbox hook install: backup %s: %v\n", path, err)
			return 1
		}
		fmt.Printf("backup %s\n", backup)
	}
	if err := os.WriteFile(path, updated, mode); err != nil {
		fmt.Fprintln(os.Stderr, "blackbox hook install:", err)
		return 1
	}
	if *remove {
		fmt.Printf("removed the blackbox hook from %s\n", path)
	} else {
		fmt.Printf("wrote %s\nClaude Code sessions started from now on are recorded; see them with: blackbox sessions\n", path)
	}
	return 0
}

func hookSettingsPath(scope, dir string) (string, error) {
	if scope == "user" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".claude", "settings.json"), nil
	}
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		dir = wd
	}
	switch scope {
	case "local":
		return filepath.Join(dir, ".claude", "settings.local.json"), nil
	case "project":
		return filepath.Join(dir, ".claude", "settings.json"), nil
	}
	return "", fmt.Errorf("unknown scope %q (want local, project or user)", scope)
}

// editHookSettings returns settings with the adapter registered for every hook event,
// or with it removed. Other hooks, and the rest of the file, are left as they are. An
// earlier blackbox entry is replaced, so installing twice does not register it twice.
func editHookSettings(settings []byte, bin string, args []string, remove bool) ([]byte, error) {
	fresh := len(strings.TrimSpace(string(settings))) == 0
	if fresh {
		settings = []byte("{}")
	}
	if !gjson.ValidBytes(settings) || !gjson.ParseBytes(settings).IsObject() {
		return nil, errors.New("not a JSON object; left untouched")
	}

	// Exec form (command + args) runs the binary with no shell, so a path with spaces
	// needs no quoting on any OS.
	type handler struct {
		Type    string   `json:"type"`
		Command string   `json:"command"`
		Args    []string `json:"args"`
		Timeout int      `json:"timeout"`
	}
	type matcherGroup struct {
		Matcher string    `json:"matcher"`
		Hooks   []handler `json:"hooks"`
	}
	ours, err := json.MarshalIndent(matcherGroup{
		Matcher: "*",
		Hooks:   []handler{{Type: "command", Command: bin, Args: args, Timeout: hookTimeoutSeconds}},
	}, "      ", "  ")
	if err != nil {
		return nil, err
	}

	out := settings
	for _, event := range hookEvents {
		path := "hooks." + event
		current := gjson.GetBytes(out, path)
		if current.Exists() && !current.IsArray() {
			return nil, fmt.Errorf("%s is not an array; left untouched", path)
		}

		var groups []string
		for _, group := range current.Array() {
			kept, changed := withoutBlackboxHandlers(group)
			switch {
			case !changed:
				groups = append(groups, group.Raw)
			case kept != "":
				groups = append(groups, kept)
			}
		}
		if !remove {
			groups = append(groups, string(ours))
		}

		if len(groups) == 0 {
			if current.Exists() {
				if out, err = sjson.DeleteBytes(out, path); err != nil {
					return nil, err
				}
			}
			continue
		}
		raw := "[\n      " + strings.Join(groups, ",\n      ") + "\n    ]"
		if out, err = sjson.SetRawBytes(out, path, []byte(raw)); err != nil {
			return nil, err
		}
	}
	if remove && gjson.GetBytes(out, "hooks").Raw != "" && len(gjson.GetBytes(out, "hooks").Map()) == 0 {
		if out, err = sjson.DeleteBytes(out, "hooks"); err != nil {
			return nil, err
		}
	}
	if fresh {
		// Nothing of the user's to preserve, so lay the new file out properly.
		return pretty.Pretty(out), nil
	}
	if sameJSON(settings, out) {
		// Already installed: hand back the user's bytes, not a reformatted copy.
		return settings, nil
	}
	return out, nil
}

func sameJSON(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

// withoutBlackboxHandlers drops the adapter's handlers from one matcher group. It
// returns the group's new JSON, or "" when nothing is left in it, and whether anything
// was dropped.
func withoutBlackboxHandlers(group gjson.Result) (string, bool) {
	handlers := group.Get("hooks")
	if !handlers.IsArray() {
		return group.Raw, false
	}
	var kept []string
	changed := false
	for _, h := range handlers.Array() {
		if isBlackboxHandler(h) {
			changed = true
			continue
		}
		kept = append(kept, h.Raw)
	}
	if !changed {
		return group.Raw, false
	}
	if len(kept) == 0 {
		return "", true
	}
	raw, err := sjson.SetRaw(group.Raw, "hooks", "["+strings.Join(kept, ", ")+"]")
	if err != nil {
		return group.Raw, false
	}
	return raw, true
}

// isBlackboxHandler recognises an entry this command wrote: a blackbox binary whose
// first argument is "hook".
func isBlackboxHandler(h gjson.Result) bool {
	if h.Get("type").String() != "command" || h.Get("args.0").String() != "hook" {
		return false
	}
	name := filepath.Base(strings.ReplaceAll(h.Get("command").String(), `\`, "/"))
	return strings.TrimSuffix(strings.ToLower(name), ".exe") == "blackbox"
}
