package daemon

import (
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"os/exec"
	"bytes"

	log "github.com/Sirupsen/logrus"
	"github.com/docker/docker/daemon/execdriver"
	"github.com/docker/docker/pkg/chrootarchive"
	"github.com/docker/docker/pkg/symlink"
	"github.com/docker/docker/volumes"
	"github.com/docker/libcontainer/label"
)

type Mount struct {
	MountToPath string
	container   *Container
	volume      *volumes.Volume
	Writable    bool
	Ceph        bool
	copyData    bool
	from        *Container
}

func (mnt *Mount) Export(resource string) (io.ReadCloser, error) {
	var name string
	if resource == mnt.MountToPath[1:] {
		name = filepath.Base(resource)
	}
	path, err := filepath.Rel(mnt.MountToPath[1:], resource)
	if err != nil {
		return nil, err
	}
	return mnt.volume.Export(path, name)
}

func (container *Container) prepareVolumes(isStarting bool) error {
	if container.Volumes == nil || len(container.Volumes) == 0 {
		container.Volumes = make(map[string]string)
		container.VolumesRW = make(map[string]bool)
		container.VolumesCephDevice = make(map[string]string)
	}

	return container.createVolumes(isStarting)
}

// sortedVolumeMounts returns the list of container volume mount points sorted in lexicographic order
func (container *Container) sortedVolumeMounts() []string {
	var mountPaths []string
	for path := range container.Volumes {
		mountPaths = append(mountPaths, path)
	}

	sort.Strings(mountPaths)
	return mountPaths
}

func (container *Container) createVolumes(isStarting bool) error {
	mounts, err := container.parseVolumeMountConfig()
	if err != nil {
		return err
	}

	for _, mnt := range mounts {
		if err := mnt.initialize(isStarting); err != nil {
			return err
		}
	}

	// On every start, this will apply any new `VolumesFrom` entries passed in via HostConfig, which may override volumes set in `create`
	return container.applyVolumesFrom(isStarting)
}

func (m *Mount) initialize(isStarting bool) error {
	fmt.Printf("Initializing mount: %s -> %s (%s) %t %t\n", m.volume.Path, m.container.basefs, m.MountToPath, m.Ceph, isStarting)

	//TODO: Is it correct to do this here, or should we consider the existence check below?
	v := m.volume
	if (v.CephVolume != "" && isStarting) {
		modeOption := "rw"
		if (!v.Writable) {
			modeOption = "ro"
		}
		fmt.Printf("Mapping %s as %s\n", v.CephVolume, modeOption)
		cmd := exec.Command("rbd", "map", v.CephVolume, "--options", modeOption)
		var out bytes.Buffer
		cmd.Stderr = &out
		err := cmd.Run()
		if err == nil {
			fmt.Printf("Succeeded executing rbd\n")
		} else {
			fmt.Printf("Error executing rbd: %s - %s\n", err, out.String())
		}

		modeFlag := "--rw"
		if (!v.Writable) {
			modeFlag = "--read-only"
		}
		fmt.Printf("Mounting %s to %s on host as %s\n", v.CephDevice, v.Path, modeFlag)
		cmd = exec.Command("mount", modeFlag, v.CephDevice, v.Path)
		out.Reset()
		cmd.Stderr = &out
		err = cmd.Run()
		if err == nil {
			fmt.Printf("Succeeded mounting\n")
		} else {
			fmt.Printf("Error mounting: %s - %s\n", err, out.String())
		}
	}

	// No need to initialize anything since it's already been initialized
	if hostPath, exists := m.container.Volumes[m.MountToPath]; exists {
		// If this is a bind-mount/volumes-from, maybe it was passed in at start instead of create
		// We need to make sure bind-mounts/volumes-from passed on start can override existing ones.
		if !m.volume.IsBindMount && m.from == nil {
			return nil
		}
		if m.volume.Path == hostPath {
			return nil
		}

		// Make sure we remove these old volumes we don't actually want now.
		// Ignore any errors here since this is just cleanup, maybe someone volumes-from'd this volume
		v := m.container.daemon.volumes.Get(hostPath)
		v.RemoveContainer(m.container.ID)
		m.container.daemon.volumes.Delete(v.Path)
	}

	// This is the full path to container fs + mntToPath
	containerMntPath, err := symlink.FollowSymlinkInScope(filepath.Join(m.container.basefs, m.MountToPath), m.container.basefs)
	if err != nil {
		return err
	}
	m.container.Volumes[m.MountToPath] = m.volume.Path
	m.container.VolumesRW[m.MountToPath] = m.Writable
	m.container.VolumesCephDevice[m.MountToPath] = m.volume.CephDevice
	m.volume.AddContainer(m.container.ID)
	if m.Writable && m.copyData {
		// Copy whatever is in the container at the mntToPath to the volume
		copyExistingContents(containerMntPath, m.volume.Path)
	}

	return nil
}

func (container *Container) VolumePaths() map[string]struct{} {
	var paths = make(map[string]struct{})
	for _, path := range container.Volumes {
		paths[path] = struct{}{}
	}
	return paths
}

func (container *Container) registerVolumes() {
	for path := range container.VolumePaths() {
		if v := container.daemon.volumes.Get(path); v != nil {
			v.AddContainer(container.ID)
			continue
		}

		// if container was created with an old daemon, this volume may not be registered so we need to make sure it gets registered
		writable := true
		if rw, exists := container.VolumesRW[path]; exists {
			writable = rw
		}
		ceph := false
		if cephDevice, exists := container.VolumesCephDevice[path]; exists {
			ceph = cephDevice != ""
		}
		v, err := container.daemon.volumes.FindOrCreateVolume(path, writable, ceph)
		if err != nil {
			log.Debugf("error registering volume %s: %v", path, err)
			continue
		}
		v.AddContainer(container.ID)
	}
}

func (container *Container) derefVolumes() {
	for path := range container.VolumePaths() {
		vol := container.daemon.volumes.Get(path)
		if vol == nil {
			log.Debugf("Volume %s was not found and could not be dereferenced", path)
			continue
		}
		vol.RemoveContainer(container.ID)
	}
}

func (container *Container) parseVolumeMountConfig() (map[string]*Mount, error) {
	var mounts = make(map[string]*Mount)
	// Get all the bind mounts
	for _, spec := range container.hostConfig.Binds {
		path, mountToPath, writable, ceph, err := parseBindMountSpec(spec)
		if err != nil {
			return nil, err
		}
		// Check if a bind mount has already been specified for the same container path
		if m, exists := mounts[mountToPath]; exists {
			return nil, fmt.Errorf("Duplicate volume %q: %q already in use, mounted from %q", path, mountToPath, m.volume.Path)
		}
		// Check if a volume already exists for this and use it
		vol, err := container.daemon.volumes.FindOrCreateVolume(path, writable, ceph)
		if err != nil {
			return nil, err
		}
		mounts[mountToPath] = &Mount{
			container:   container,
			volume:      vol,
			MountToPath: mountToPath,
			Writable:    writable,
			Ceph:        ceph,
		}
	}

	// Get the rest of the volumes
	for path := range container.Config.Volumes {
		// Check if this is already added as a bind-mount
		path = filepath.Clean(path)
		if _, exists := mounts[path]; exists {
			continue
		}

		// Check if this has already been created
		if _, exists := container.Volumes[path]; exists {
			continue
		}

		if stat, err := os.Stat(filepath.Join(container.basefs, path)); err == nil {
			if !stat.IsDir() {
				return nil, fmt.Errorf("file exists at %s, can't create volume there")
			}
		}

		vol, err := container.daemon.volumes.FindOrCreateVolume("", true, false)
		if err != nil {
			return nil, err
		}
		mounts[path] = &Mount{
			container:   container,
			MountToPath: path,
			volume:      vol,
			Writable:    true,
			Ceph:        false,
			copyData:    true,
		}
	}

	return mounts, nil
}

func parseBindMountSpec(spec string) (string, string, bool, bool, error) {
	var (
		path, mountToPath string
		writable          bool
		ceph              bool
		arr               = strings.Split(spec, ":")
	)

	switch len(arr) {
	case 2:
		path = arr[0]
		mountToPath = arr[1]
		writable = true
		ceph = false
	case 3:
		path = arr[0]
		mountToPath = arr[1]
		writable, ceph = parseMountOptions(arr[2])
	default:
		return "", "", false, false, fmt.Errorf("Invalid volume specification: %s", spec)
	}

	//TODO: If ceph, check that path is a valid ceph volume name
	if !ceph {
		if !filepath.IsAbs(path) {
			return "", "", false, false, fmt.Errorf("cannot bind mount volume: %s volume paths must be absolute.", path)
		} else {
			path = filepath.Clean(path)
		}
	}

	mountToPath = filepath.Clean(mountToPath)
	fmt.Printf("Volume bind: %s : %s : %s : %s\n", path, mountToPath, writable, ceph)
	return path, mountToPath, writable, ceph, nil
}

func parseVolumesFromSpec(spec string) (string, string, error) {
	specParts := strings.SplitN(spec, ":", 2)
	if len(specParts) == 0 {
		return "", "", fmt.Errorf("malformed volumes-from specification: %s", spec)
	}

	var (
		id   = specParts[0]
		mode = "rw"
	)
	if len(specParts) == 2 {
		mode = specParts[1]
		if !validMountMode(mode) {
			return "", "", fmt.Errorf("invalid mode for volumes-from: %s", mode)
		}
	}
	return id, mode, nil
}

func (container *Container) applyVolumesFrom(isStarting bool) error {
	volumesFrom := container.hostConfig.VolumesFrom
	if len(volumesFrom) > 0 && container.AppliedVolumesFrom == nil {
		container.AppliedVolumesFrom = make(map[string]struct{})
	}

	mountGroups := make(map[string][]*Mount)

	for _, spec := range volumesFrom {
		id, mode, err := parseVolumesFromSpec(spec)
		if err != nil {
			return err
		}
		if _, exists := container.AppliedVolumesFrom[id]; exists {
			// Don't try to apply these since they've already been applied
			continue
		}

		c, err := container.daemon.Get(id)
		if err != nil {
			return err
		}

		var (
			fromMounts = c.VolumeMounts()
			mounts     []*Mount
		)

		for _, mnt := range fromMounts {
			mnt.Writable = mnt.Writable && (mode == "rw")
			mounts = append(mounts, mnt)
		}
		mountGroups[id] = mounts
	}

	for id, mounts := range mountGroups {
		for _, mnt := range mounts {
			mnt.from = mnt.container
			mnt.container = container
			if err := mnt.initialize(isStarting); err != nil {
				return err
			}
		}
		container.AppliedVolumesFrom[id] = struct{}{}
	}
	return nil
}

func validMountMode(mode string) bool {
	validModes := map[string]bool{
		"rw": true,
		"ro": true,
	}

	return validModes[mode]
}

func parseMountOptions(options string) (bool, bool) {
	var (
		writable = false
		ceph = false
	)
	for _, option := range strings.Split(options, ",") {
		if option == "ro" {
			writable = false
		} else if option == "rw" {
			writable = true
		} else if option == "ceph" {
			ceph = true
		}
	}
	return writable, ceph
}

func (container *Container) setupMounts() error {
	mounts := []execdriver.Mount{
		{Source: container.ResolvConfPath, Destination: "/etc/resolv.conf", Writable: true, Private: true},
	}

	if container.HostnamePath != "" {
		mounts = append(mounts, execdriver.Mount{Source: container.HostnamePath, Destination: "/etc/hostname", Writable: true, Private: true})
	}

	if container.HostsPath != "" {
		mounts = append(mounts, execdriver.Mount{Source: container.HostsPath, Destination: "/etc/hosts", Writable: true, Private: true})
	}

	for _, m := range mounts {
		if err := label.SetFileLabel(m.Source, container.MountLabel); err != nil {
			return err
		}
	}

	// Mount user specified volumes
	// Note, these are not private because you may want propagation of (un)mounts from host
	// volumes. For instance if you use -v /usr:/usr and the host later mounts /usr/share you
	// want this new mount in the container
	// These mounts must be ordered based on the length of the path that it is being mounted to (lexicographic)
	for _, path := range container.sortedVolumeMounts() {
		mounts = append(mounts, execdriver.Mount{
			Source:      container.Volumes[path],
			Destination: path,
			Writable:    container.VolumesRW[path],
		})
	}

	container.command.Mounts = mounts
	return nil
}

func (container *Container) VolumeMounts() map[string]*Mount {
	mounts := make(map[string]*Mount)

	for mountToPath, path := range container.Volumes {
		if v := container.daemon.volumes.Get(path); v != nil {
			mounts[mountToPath] = &Mount{volume: v, container: container, MountToPath: mountToPath, Writable: container.VolumesRW[mountToPath]}
		}
	}

	return mounts
}

func copyExistingContents(source, destination string) error {
	volList, err := ioutil.ReadDir(source)
	if err != nil {
		return err
	}

	if len(volList) > 0 {
		srcList, err := ioutil.ReadDir(destination)
		if err != nil {
			return err
		}

		if len(srcList) == 0 {
			// If the source volume is empty copy files from the root into the volume
			if err := chrootarchive.CopyWithTar(source, destination); err != nil {
				return err
			}
		}
	}

	return copyOwnership(source, destination)
}

// copyOwnership copies the permissions and uid:gid of the source file
// into the destination file
func copyOwnership(source, destination string) error {
	var stat syscall.Stat_t

	if err := syscall.Stat(source, &stat); err != nil {
		return err
	}

	if err := os.Chown(destination, int(stat.Uid), int(stat.Gid)); err != nil {
		return err
	}

	return os.Chmod(destination, os.FileMode(stat.Mode))
}
