//go:build !linux

// Non-Linux builds exist only for local UI development with explicitly
// registered dev roots. All syscall-backed operations fail closed.
package resolver

import (
	"github.com/turlamai/trove/daemon/internal/roots"
	"github.com/turlamai/trove/daemon/internal/troveerr"
)

func probeOpenat2() bool { return false }

func isEscapeErr(error) bool { return true } // fail closed on non-Linux

func unsupported(op string) error {
	return troveerr.New(troveerr.Unsupported, op+" requires Linux (daemon is a Unraid component)")
}

func OpenRoot(string) (int, error) { return -1, unsupported("OpenRoot") }
func Openat2Beneath(int, string) (int, error) { return -1, unsupported("Openat2Beneath") }
func CloseFD(int) {}
func FstatAt(int, string) (*StatInfo, error) { return nil, unsupported("FstatAt") }
func ReadLinkAt(int, string) (string, error) { return "", unsupported("ReadLinkAt") }
func ListDir(int) ([]DirEntry, error) { return nil, unsupported("ListDir") }
func OpenDirNoFollow(int, string) (int, error) { return -1, unsupported("OpenDirNoFollow") }
func OpenFileNoFollow(int, string, int, uint32) (int, error) { return -1, unsupported("OpenFileNoFollow") }
func CreateFileExclusive(int, string, uint32) (int, error) { return -1, unsupported("CreateFileExclusive") }
func MkdirAt(int, string, uint32) error { return unsupported("MkdirAt") }
func UnlinkAt(int, string) error { return unsupported("UnlinkAt") }
func RmdirAt(int, string) error { return unsupported("RmdirAt") }
func RenameAt(int, string, int, string, int) error { return unsupported("RenameAt") }
func StatFSAt(string) (int64, int64, error) { return 0, 0, unsupported("StatFSAt") }
func FsyncFD(int) error { return unsupported("FsyncFD") }
func FchmodFD(int, uint32) error { return unsupported("FchmodFD") }
func FchownFD(int, uint32, uint32) error { return unsupported("FchownFD") }
func SetTimesFD(int, *StatInfo) error { return unsupported("SetTimesFD") }
func CopyRange(int, int, int64) (int64, error) { return 0, unsupported("CopyRange") }
func ENOTEmpty(error) bool { return false }

// RenameNoReplace is unused on non-Linux builds.
const RenameNoReplace = 1

var _ FSView = (*Resolver)(nil)

func (r *Resolver) StatNode(*roots.Root, string) (*StatInfo, error) {
	return nil, unsupported("StatNode")
}
func (r *Resolver) Walk(*roots.Root, string, WalkFunc, WalkCaps) (bool, error) {
	return false, unsupported("Walk")
}
func (r *Resolver) StatFS(*roots.Root) (int64, int64, error) {
	return 0, 0, unsupported("StatFS")
}

func SeekFD(int, int64) (int64, error) { return 0, unsupported("SeekFD") }
func WriteAllFD(int, []byte) error { return unsupported("WriteAllFD") }
func ReadFD(int, []byte) (int, error) { return 0, unsupported("ReadFD") }
