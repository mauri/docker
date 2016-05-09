package nfsvolumedriver

import (
	"errors"
	"fmt"
	"github.com/Sirupsen/logrus"
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
	m sync.Mutex

	// Amount of container mounts using this volume
	usedCount int
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
	v.m.Lock()
	defer v.m.Unlock()
	return v.hostFolder
}

func (v *Volume) Mount() (string, error) {
	v.m.Lock()
	defer v.m.Unlock()

	// Even if Mount() fails, Unmount will be called.
	// So we increment usedCount ASAP to maintain the value
	// in a coherent way
	v.usedCount++
	if v.usedCount > 1 {
		// Already mounted
		return v.hostFolder, nil
	}
	name, err := ioutil.TempDir(NFS_MOUNTS_FOLDER, "")
	if err != nil {
		return "", err
	}
	v.hostFolder = name
	// retry=0,timeo=30: Fail if NFS server can't be reached in 30 second (no retries) - aggressive, but necessary because the Docker daemon becomes unresponsive if the mount command hangs.
	args := []string{"-o", "retry=0,timeo=30"}

	if err = libcontainer.DoMountCmd(v.DriverName(), v.Name(), v.hostFolder, args); err != nil {
		return "", err
	}
	return v.hostFolder, nil
}

func (v *Volume) Unmount() error {
	v.m.Lock()
	defer v.m.Unlock()

	if err := v.release(); err != nil {
		return err
	}

	// Don't unmount if still being used
	if v.usedCount > 0 {
		return nil
	}

	err := exec.Command("umount", v.hostFolder).Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to unmount nfs device %s from %s\n", v.Name(), v.hostFolder)
		return err
	}

	err = os.Remove(v.hostFolder)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to remove folder %s\n", v.hostFolder)
	}
	v.hostFolder = ""
	return err
}

func (v *Volume) release() error {
	// Note that the call to release() is assumed to be contained in a v.m.Lock()/Unlock() (the mutex isn't reentrant, so we can't lock it again here)
	if v.usedCount == 0 { // Shouldn't happen as long as Docker calls Mount()/Unmount() the way we think, but we've misunderstood the call sequence before
		msg := fmt.Sprintf("Bug: The nfs volume '%s' is being released more times than it has been used", v.Name())
		logrus.Errorf(msg)
		return errors.New(msg)
	}
	v.usedCount--
	return nil
}
