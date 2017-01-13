// +build !windows

package daemon

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/docker/docker/container"
	"github.com/docker/docker/volume"
)

// setupMounts iterates through each of the mount points for a container and
// calls Setup() on each. It also looks to see if is a network mount such as
// /etc/resolv.conf, and if it is not, appends it to the array of mounts.
func (daemon *Daemon) setupMounts(c *container.Container) ([]container.Mount, error) {
	var mounts []container.Mount
	// TODO: tmpfs mounts should be part of Mountpoints
	tmpfsMounts := make(map[string]bool)
	for _, m := range c.TmpfsMounts() {
		tmpfsMounts[m.Destination] = true
	}
	for _, m := range c.MountPoints {
		if tmpfsMounts[m.Destination] {
			continue
		}
		if err := daemon.lazyInitializeVolume(c.ID, m); err != nil {
			return nil, err
		}
		path, err := m.Setup(c.MountLabel)
		if err != nil {
			return nil, err
		}
		if !c.TrySetNetworkMount(m.Destination, path) {
			mnt := container.Mount{
				Source:      path, // Note that for Ceph volumes, this will be the mapped device (e.g. /dev/rbd0), and for NFS shares, it will be the share URI (e.g. 1.2.3.4://foo)
				Destination: m.Destination,
				Writable:    m.RW,
				Propagation: m.Propagation,
			}
			if m.Volume != nil {
				attributes := map[string]string{
					"driver":      m.Volume.DriverName(),
					"container":   c.ID,
					"destination": m.Destination,
					"read/write":  strconv.FormatBool(m.RW),
					"propagation": m.Propagation,
				}
				daemon.LogVolumeEvent(m.Volume.Name(), "mount", attributes)
			}
			if m.Driver == "nfs" || m.Driver == "ceph" {
				mnt.Data = m.Driver
				if m.Driver == "nfs" {
					mnt.Source = strings.Replace(path, "//", "://", 1)
				}
			}
			mounts = append(mounts, mnt)
		}
	}

	mounts = sortMounts(mounts)
	netMounts := c.NetworkMounts()
	// if we are going to mount any of the network files from container
	// metadata, the ownership must be set properly for potential container
	// remapped root (user namespaces)
	rootUID, rootGID := daemon.GetRemappedUIDGID()
	for _, mount := range netMounts {
		if err := os.Chown(mount.Source, rootUID, rootGID); err != nil {
			return nil, err
		}
	}
	return append(mounts, netMounts...), nil
}

func parseMountMode(mode string) (bool, string, string, error) {
	rw := false
	rwSpecified := false
	sharingSpecified := false
	var labelItems []string
	driver := ""
	for _, item := range strings.Split(mode, ",") {
		switch item {
		case "rw", "ro":
			if rwSpecified {
				return false, "", "", fmt.Errorf("invalid mode for volumes: %s", mode)
			}
			rw = item == "rw"
			rwSpecified = true
			labelItems = append(labelItems, item)
		case "z", "Z":
			if sharingSpecified {
				return false, "", "", fmt.Errorf("invalid mode for volumes: %s", mode)
			}
			sharingSpecified = true
			labelItems = append(labelItems, item)
		case "ceph", "nfs":
			if driver != "" {
				return false, "", "", fmt.Errorf("invalid mode for volumes: %s", mode)
			}
			driver = item
		}
	}
	return rw, strings.Join(labelItems, ","), driver, nil
}

// sortMounts sorts an array of mounts in lexicographic order. This ensure that
// when mounting, the mounts don't shadow other mounts. For example, if mounting
// /etc and /etc/resolv.conf, /etc/resolv.conf must not be mounted first.
func sortMounts(m []container.Mount) []container.Mount {
	sort.Sort(mounts(m))
	return m
}

// setBindModeIfNull is platform specific processing to ensure the
// shared mode is set to 'z' if it is null. This is called in the case
// of processing a named volume and not a typical bind.
func setBindModeIfNull(bind *volume.MountPoint) *volume.MountPoint {
	if bind.Mode == "" {
		bind.Mode = "z"
	}
	return bind
}
