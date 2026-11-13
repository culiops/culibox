package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/culiops/culibox/internal/abduco"
	"github.com/culiops/culibox/internal/account"
	"github.com/culiops/culibox/internal/analytics"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/credfile"
	"github.com/culiops/culibox/internal/instancelock"
	"github.com/culiops/culibox/internal/rootmove"
	"github.com/culiops/culibox/internal/secretstore"
	"github.com/culiops/culibox/internal/session"
)

// newKeeperForHash builds the keeper for a KeyHash (a resumed move reads the
// old key by the hash its journal recorded); swapped in tests like newKeeper.
var newKeeperForHash = secretstore.NewKeeperForHash

// rootmoveXDG is rootmove.MoveXDGConfig; a variable so a test can see it run.
var rootmoveXDG = rootmove.MoveXDGConfig

// maybeMoveDataRoot is main's gate: only `serve`, and only with no root env
// var (spec D2 precondition (a)), moves anything. With an explicit -config the
// config serve loads is a file the move cannot inspect (the data_dir skip and
// precondition (e) read the default one), so neither the root nor the
// ~/.config file starts to move; the reason is logged and reaches doctor. An
// interrupted move is still finished under -config (ruling R14): the resume
// reads its journal and the default config, not the config serve loads. It is
// finished only when that instance's data dir is the new root (#273): only then
// is the folder holding the journal protected from its boxes; otherwise the
// journal could be a box's.
func maybeMoveDataRoot(cmd, configPath string, configSet bool, envHome, home, xdg, goos string, logf func(string, ...any)) (rootmove.Outcome, error) {
	if cmd != "serve" || envHome != "" {
		return rootmove.Outcome{}, nil
	}
	if configSet {
		if newRoot, legacy := config.DefaultRoots(home); rootmove.Pending(newRoot, legacy) {
			if why := notNewRoot(configPath, newRoot); why != "" {
				logf("data root: the interrupted move in %s was not finished (%s); a serve start that uses that root finishes it", newRoot, why)
				return rootmove.Outcome{Reason: why}, nil
			}
			return runRootMove(home, xdg, goos, logf)
		}
		out := rootmove.Outcome{Reason: "serve -config is set; the move only reads the default config"}
		if ri, err := config.ResolveRoot(home, ""); err == nil && ri.Kind == config.RootLegacy {
			logf("data root: kept at %s (%s); the next serve start tries again", ri.Path, out.Reason)
		}
		return out, nil
	}
	return moveDataRoot(home, xdg, goos, logf)
}

// moveDataRoot is serve's one-time data root move (spec D2): it runs in main
// before config.Resolve, on EVERY start so an interrupted move (a journal in the
// new root) is finished. With nothing to move it does nothing. The outcome's
// Reason says why the old root stayed in place ("" when moved or when there was
// nothing to move); an error must stop serve.
//
// A root or config that cannot be resolved skips the move: the normal start
// then reports it. The ~/.config file moves first, only while this process
// holds the instance lock on the root in use (spec: preconditions (a) and (c)).
func moveDataRoot(home, xdg, goos string, logf func(string, ...any)) (rootmove.Outcome, error) {
	root, err := config.Root(home, "")
	if err != nil {
		return rootmove.Outcome{}, nil
	}
	relinked := moveXDGConfig(config.XDGBase(home, xdg), root, logf)
	out, err := runRootMove(home, xdg, goos, logf)
	out.XDGRelinked = relinked
	return out, err
}

// runRootMove runs rootmove.Run on the default roots with the default config's
// deps (the config a move without -config resolves).
func runRootMove(home, xdg, goos string, logf func(string, ...any)) (rootmove.Outcome, error) {
	cfg, src, err := config.Resolve("", false, home, xdg, "")
	if err != nil {
		return rootmove.Outcome{}, nil
	}
	newRoot, legacy := config.DefaultRoots(home)
	var out rootmove.Outcome
	if src.Root.Kind == config.RootLegacy && !src.Default && setsDataDir(src.Path) {
		out = rootmove.Outcome{Reason: "config.json sets data_dir"}
	} else {
		cfgText, _ := os.ReadFile(src.Path) // "" with built-in defaults
		credText, _ := os.ReadFile(credfile.Path(legacy))
		out, err = rootmove.Run(newRoot, legacy, rootmove.Deps{
			GOOS:          goos,
			Keeper:        newKeeper,
			KeeperForHash: newKeeperForHash,
			Live: func() (int, error) {
				return abduco.LiveSessions(abduco.SocketDirFor(cfg.AbducoSocketDir, goos, legacy, os.Getuid()),
					filepath.Join(legacy, "sessions", "pids"))
			},
			Lock:        instancelock.Acquire,
			Rewrite:     rootRewriters(cfg),
			ConfigTexts: []string{string(cfgText), string(credText)}, // config.json, then credentials.json: rootmove names them by position
			Logf:        logf,
		})
		if err != nil {
			return out, err
		}
	}
	if !out.Moved && out.Reason != "" {
		logf("data root: kept at %s (%s); the next serve start tries again", src.Root.Path, out.Reason)
	}
	return out, nil
}

// moveXDGConfig moves ~/.config/agent-box/config.json to ~/.config/culibox/
// while this process holds the instance lock on root: another serve there is a
// reason to wait. A failure is logged and never stops serve; the link (or the
// untouched file) keeps the config found. relinked reports a step that only made
// the missing link beside a culibox file that was already there: that says
// nothing about where the file came from (#266).
func moveXDGConfig(xdgBase, root string, logf func(string, ...any)) (relinked bool) {
	release, err := instancelock.Acquire(root)
	if errors.Is(err, instancelock.ErrLocked) {
		logf("config: the ~/.config file was not moved: another serve holds %s", root)
		return false
	}
	if err != nil {
		logf("config: the ~/.config file was not moved: %v", err)
		return false
	}
	defer release()
	oldF := filepath.Join(xdgBase, config.LegacyRootName, "config.json")
	newF := filepath.Join(xdgBase, config.RootName, "config.json")
	_, oerr := os.Lstat(oldF)
	_, nerr := os.Lstat(newF)
	moved, err := rootmoveXDG(xdgBase)
	switch {
	case err != nil:
		logf("WARNING: config file move: %v", err)
	case moved && nerr == nil && errors.Is(oerr, os.ErrNotExist):
		logf("config: a link was left at %s for %s", oldF, newF)
		return true
	case moved:
		logf("config moved: %s -> %s (a link was left at the old path)", oldF, newF)
	}
	return false
}

// startFacts is what a start's moves did, for config.Source.UnvettedWarning.
func startFacts(out rootmove.Outcome) config.StartFacts {
	return config.StartFacts{RootMoved: out.Moved, ConfigRelinked: out.XDGRelinked}
}

// serveStartWarnings logs the WARN for a config read from the culibox ~/.config
// file before the data root moved (#266). Only `serve` logs it (doctor has its
// own row), and only for a config that loaded: main calls it right after
// config.Resolve, so a later refusal (validateBoot) cannot skip it.
func serveStartWarnings(cmd string, src config.Source, cfgErr error, f config.StartFacts, logf func(string, ...any)) {
	if cmd != "serve" || cfgErr != nil {
		return
	}
	if w := src.UnvettedWarning(f); w != "" {
		logf("WARNING: %s", w)
	}
}

// notNewRoot says why the -config file at path does not use the new default
// root ("" when it does: no data_dir, which resolves to it while a move is
// pending, or a data_dir naming it). A data_dir naming the legacy path does
// not: a pending move has emptied it, and protecting it does not protect the
// new root. An unreadable file uses none; the config step then reports it.
func notNewRoot(path, newRoot string) string {
	c, err := config.Load(path)
	if err != nil {
		return "serve -config could not be read"
	}
	if c.DataDir == "" {
		return ""
	}
	d, err := filepath.Abs(c.DataDir)
	if err == nil && d == newRoot {
		return ""
	}
	return fmt.Sprintf("serve -config sets data_dir %q", c.DataDir)
}

// setsDataDir reports whether the config file at path sets data_dir.
func setsDataDir(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var c struct {
		DataDir string `json:"data_dir"`
	}
	return json.Unmarshal(b, &c) == nil && c.DataDir != ""
}

// rootRewriters are the stored-path rewriters a move runs against the new root
// after the rename: the analytics ingest state, the session sidecar and each
// account's status line.
func rootRewriters(cfg config.Config) []func(oldRoot, newRoot string) error {
	return []func(oldRoot, newRoot string) error{
		func(oldRoot, newRoot string) error {
			p := cfg.Analytics.DBPath
			if p == "" {
				p = filepath.Join(newRoot, "analytics.db")
			}
			if _, err := os.Stat(p); errors.Is(err, os.ErrNotExist) {
				return nil
			} else if err != nil {
				return err
			}
			st, err := analytics.Open(p)
			if err != nil {
				return err
			}
			defer st.Close()
			_, err = st.RewritePathPrefix(oldRoot, newRoot)
			return err
		},
		func(oldRoot, newRoot string) error {
			return session.RewriteSidecarPrefix(filepath.Join(newRoot, "sessions.json"), oldRoot, newRoot)
		},
		func(oldRoot, newRoot string) error {
			return account.RewriteStatusLinePrefix(newRoot, oldRoot, newRoot)
		},
	}
}
