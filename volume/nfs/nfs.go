package nfsvolumedriver

import (
	"fmt"
	"github.com/docker/docker/volume"
	"github.com/opencontainers/runc/libcontainer"
	"io/ioutil"
	"os"
	"os/exec"
	"sync"
)

const (
	NFS_MOUNTS_FOLDER             = "/var/lib/docker/nfs_mounts"
	NFS_MOUNTS_FOLDER_PERMISSIONS = 0755
)

func New() *Root {
	return &Root{}
}

type Root struct {
	m sync.Mutex
}

func (r *Root) Name() string {
	return "nfs"
}

// Makes sure that the nfs mounts folder exists
func ensureNfsFolderExists() error {
	_, err := os.Stat(NFS_MOUNTS_FOLDER)
	if err == nil {
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	return os.MkdirAll(NFS_MOUNTS_FOLDER, NFS_MOUNTS_FOLDER_PERMISSIONS)
}

func (r *Root) Create(name string, _ map[string]string) (volume.Volume, error) {
	r.m.Lock()
	defer r.m.Unlock()

	ensureNfsFolderExists()
	return &Volume{
		driverName: r.Name(),
		name:       name,
	}, nil
}

func (r *Root) Remove(v volume.Volume) error {
	// Nothing to do
	return nil
}

type Volume struct {
	// unique name of the volume
	name string
	// driverName is the name of the driver that created the volume.
	driverName string
	// The host folder where the nfs was mounted to
	hostFolder string
}

func (v *Volume) Name() string {
	return v.name
}

func (v *Volume) DriverName() string {
	return v.driverName
}

func (v *Volume) Path() string {
	return v.hostFolder
}

func (v *Volume) Mount() (string, error) {
	// The return value from this method will be passed to the container
	name, err := ioutil.TempDir(NFS_MOUNTS_FOLDER, "")
	if err != nil {
		return "", err
	}
	v.hostFolder = name
	// retry=0,timeo=30: Fail if NFS server can't be reached in three second (no retries) - aggressive, but necessary because the Docker daemon becomes unresponsive if the mount command hangs.
	// nolock:           Don't use NFS locking, because the host's rpc.statd can't be reached at this point since we're already inside the network namespace.
	//                   This won't let us use fcntl, but that's on par with today's system, since our current NFS server doesn't support locking.
	args := []string{"-o", "retry=0,timeo=30,nolock"}

	if err = libcontainer.DoMountCmd(v.DriverName(), v.Name(), v.Path(), args); err != nil {
		return "", err
	}
	return v.hostFolder, nil
}

func (v *Volume) Unmount() error {
	err := exec.Command("umount", v.Path()).Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to unmount nfs device %s to %s\n", v.Name(), v.Path())
	}
	return err
}
