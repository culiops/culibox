package doctor

import (
	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/credref"
	"github.com/culiops/agent-box/internal/secretstore"
	"github.com/culiops/agent-box/internal/storerefs"
)

// StoreFor opens the encrypted store under dataDir for a config that reads from
// it, read-only and without an audit recorder (doctor changes nothing). It
// returns an untyped nil for a config that does not use the store, so callers
// can pass the result on without a typed-nil interface. doctor runs in its own
// process and reads the same kernel keyring entry the daemon does.
func StoreFor(c config.Config, dataDir string) credref.StoreGetter {
	if !storerefs.InUse(c) {
		return nil
	}
	return secretstore.Open(dataDir, secretstore.NewKeeper(dataDir), nil)
}

// defaultResolver resolves file, env and store references, the way the daemon
// does. A nil store refuses store references.
func defaultResolver(st credref.StoreGetter) credref.Default {
	d := credref.Default{}
	if st != nil {
		d.Store = st
	}
	return d
}
