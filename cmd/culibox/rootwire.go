package main

import (
	"encoding/json"
	"errors"
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

// maybeMoveDataRoot is main's gate: only `serve`, and only with no root env var
// (spec D2 precondition (a)), moves anything.
func maybeMoveDataRoot(cmd, envHome, home, xdg, goos string, logf func(string, ...any)) (rootmove.Outcome, error) {
	if cmd != "serve" || envHome != "" {
		return rootmove.Outcome{}, nil
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
	moveXDGConfig(config.XDGBase(home, xdg), root, logf)
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
// untouched file) keeps the config found.
func moveXDGConfig(xdgBase, root string, logf func(string, ...any)) {
	release, err := instancelock.Acquire(root)
	if errors.Is(err, instancelock.ErrLocked) {
		logf("config: the ~/.config file was not moved: another serve holds %s", root)
		return
	}
	if err != nil {
		logf("config: the ~/.config file was not moved: %v", err)
		return
	}
	defer release()
	moved, err := rootmoveXDG(xdgBase)
	if err != nil {
		logf("WARNING: config file move: %v", err)
	} else if moved {
		logf("config moved: %s -> %s (a link was left at the old path)",
			filepath.Join(xdgBase, config.LegacyRootName, "config.json"),
			filepath.Join(xdgBase, config.RootName, "config.json"))
	}
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
