//go:build !linux

// Non-Linux platforms have no mount discovery. The daemon is a Linux product
// (Unraid); non-Linux builds exist for local UI development with explicitly
// registered dev roots only.
package roots

import (
	"github.com/turlamai/trove/daemon/internal/model"
	"github.com/turlamai/trove/daemon/internal/troveerr"
)

func discoverPlatform() (map[string]*Root, error) {
	return nil, troveerr.New(troveerr.Unsupported, "mount discovery requires Linux")
}

var _ = model.KindShare
