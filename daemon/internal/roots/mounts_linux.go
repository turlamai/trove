//go:build linux

// Linux mount discovery: /proc/self/mountinfo + stat. Classification:
//
//	/mnt/user        → the user-share FUSE mount; every first-level
//	                   subdirectory is a share root
//	/mnt/diskN       → array disk roots (physical view)
//	/mnt/<other>     → cache pool roots (physical view)
//
// Non-storage mounts (/mnt/addons, /mnt/rootfs, Unassigned Devices under
// /mnt/disks/...) are never registered, so they are structurally unreachable.
package roots

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/turlamai/trove/daemon/internal/model"
	"github.com/turlamai/trove/daemon/internal/troveerr"
)

const mntBase = "/mnt"

// reserved are /mnt child names that are never storage roots.
var reserved = map[string]bool{
	"user": true, "user0": true, "addons": true, "rootfs": true, "disks": true,
}

func discoverPlatform() (map[string]*Root, error) {
	mounts, err := parseMountinfo()
	if err != nil {
		return nil, err
	}

	out := map[string]*Root{}

	// User shares.
	if u, ok := mounts[mntBase+"/user"]; ok && !u.readOnly {
		entries, err := os.ReadDir(mntBase + "/user")
		if err == nil {
			for _, e := range entries {
				if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
					continue
				}
				r, err := statRoot(model.KindShare, e.Name(), filepath.Join(mntBase, "user", e.Name()))
				if err != nil {
					continue // vanished between readdir and stat — skip, don't guess
				}
				r.MountID = u.id
				out[r.Key()] = r
			}
		}
	}

	// Disk and pool mounts.
	names := make([]string, 0, len(mounts))
	for p := range mounts {
		names = append(names, strings.TrimPrefix(p, mntBase+"/"))
	}
	sort.Strings(names)
	for _, name := range names {
		if reserved[name] {
			continue
		}
		m := mounts[mntBase+"/"+name]
		if diskName(name) {
			if r, err := statRoot(model.KindDisk, name, mntBase+"/"+name); err == nil {
				r.MountID = m.id
				out[r.Key()] = r
			}
			continue
		}
		if r, err := statRoot(model.KindPool, name, mntBase+"/"+name); err == nil {
			r.MountID = m.id
			out[r.Key()] = r
		}
	}
	return out, nil
}

// diskName matches the array disk naming: disk1..disk28.
func diskName(name string) bool {
	if !strings.HasPrefix(name, "disk") || len(name) < 5 {
		return false
	}
	n := name[4:]
	if len(n) > 2 {
		return false
	}
	for i := 0; i < len(n); i++ {
		if n[i] < '0' || n[i] > '9' {
			return false
		}
	}
	return true
}

func statRoot(kind model.RootKind, id, path string) (*Root, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, troveerr.New(troveerr.Internal, "stat_t conversion failed")
	}
	return &Root{
		Ref:      kind,
		ID:       id,
		Path:     path,
		Dev:      uint64(st.Dev),
		FSType:   fsTypeOf(path),
		ReadOnly: false,
	}, nil
}

type mountEntry struct {
	id       uint32
	path     string
	readOnly bool
	fsType   string
	source   string
}

// parseMountinfo parses /proc/self/mountinfo (fstab-5 format).
func parseMountinfo() (map[string]*mountEntry, error) {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return nil, err
	}
	defer f.Close()

	out := map[string]*mountEntry{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 9 {
			continue
		}
		id, err := strconv.ParseUint(fields[0], 10, 32)
		if err != nil {
			continue
		}
		sep := -1
		for i, fld := range fields {
			if fld == "-" {
				sep = i
				break
			}
		}
		if sep < 0 || sep+2 >= len(fields) {
			continue
		}
		mountPoint := unescapeMount(fields[4])
		if !strings.HasPrefix(mountPoint, mntBase+"/") ||
			strings.Contains(mountPoint[len(mntBase)+1:], "/") {
			continue // only direct children of /mnt
		}
		opts := fields[5]
		e := &mountEntry{
			id:       uint32(id),
			path:     mountPoint,
			readOnly: false,
			fsType:   fields[sep+1],
			source:   fields[sep+2],
		}
		for _, o := range strings.Split(opts, ",") {
			if o == "ro" {
				e.readOnly = true
			}
		}
		out[mountPoint] = e
	}
	return out, sc.Err()
}

// unescapeMount decodes the octal escapes mountinfo uses for space/tab/backslash.
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// fsTypeOf reads the filesystem type for a path (used for the compatibility
// matrix display only; safety decisions use mount identity, not fs names).
func fsTypeOf(path string) string {
	mounts, err := parseMountinfo()
	if err != nil {
		return ""
	}
	best := ""
	for p, e := range mounts {
		if strings.HasPrefix(path, p+sepOrEmpty(p)) && len(p) > len(best) {
			best = p
			_ = e
		}
	}
	if e, ok := mounts[best]; ok {
		return e.fsType
	}
	return ""
}

func sepOrEmpty(p string) string {
	if p == "/" {
		return ""
	}
	return "/"
}

var _ = fmt.Sprintf // keep fmt for debug helpers
