// Command boximage-ref prints the published box image a release binary pulls by
// default: ghcr.io/culiops/culibox@<multi-arch index digest>, found under the tag
// dockerfile-<Hash> that box-image.yml adds, where Hash is this tree's
// docker/culibox Dockerfile hash (boximage.Hash). It fails when that image is
// missing or does not match (both arches, the Dockerfile label on each).
// scripts/release/build.sh bakes the ref into the binary. A build/release
// helper, not shipped in tarballs. Anonymous: the package is public.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	boximage "github.com/culiops/culibox/docker/culibox"
)

func main() {
	registry := flag.String("registry", "https://ghcr.io", "registry base URL (tests only)")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	c := &http.Client{Timeout: time.Minute}
	ref, err := resolve(ctx, c, *registry, "culiops/culibox", boximage.Hash())
	if err != nil {
		fmt.Fprintln(os.Stderr, "boximage-ref:", err)
		os.Exit(1)
	}
	fmt.Println(ref)
}
