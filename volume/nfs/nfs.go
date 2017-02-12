package nfsvolumedriver

import (
	b64 "encoding/base64"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/Sirupsen/logrus"
	"github.com/docker/docker/volume"
	"github.com/opencontainers/runc/libcontainer"
)

const (
	NFS_MOUNTS_DIRECTORY             = "/var/lib/docker/nfs_mounts"
	NFS_MOUNTS_DIRECTORY_PERMISSIONS = 0755
)

func New() *Root {
	return &Root{
		volumes: make(map[string]*Volume),
	}
}

type Root struct {
	m       sync.Mutex
	volumes map[string]*Volume
}

func (r *Root) Name() string {
	return "nfs"
}

func ensureDirectoryExists(dirName string) error {
	_, err := os.Stat(dirName)
	if err == nil {
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	return os.MkdirAll(dirName, NFS_MOUNTS_DIRECTORY_PERMISSIONS)
}

func (r *Root) Create(name string, _ map[string]string) (volume.Volume, error) {
	r.m.Lock()
	defer r.m.Unlock()

	err := ensureDirectoryExists(NFS_MOUNTS_DIRECTORY)
	if err != nil {
		return nil, err
	}

	dirName, err := ioutil.TempDir(NFS_MOUNTS_DIRECTORY, "")
	if err != nil {
		return nil, err
	}

	v := &Volume{
		driverName:    r.Name(),
		name:          b64.StdEncoding.EncodeToString([]byte(dirName)),
		hostDirectory: dirName,
		source:        name,
	}
	r.volumes[v.name] = v

	return v, nil
}

// Get looks up the volume for the given name and returns it if found
func (r *Root) Get(name string) (volume.Volume, error) {
	r.m.Lock()
	defer r.m.Unlock()
	v, ok := r.volumes[name]

	if ok {
		// we don't know exactly which element to return, so we return the first
		return v, nil
	}
	msg := fmt.Sprintf("Element not found for %s", name)
	return nil, errors.New(msg)
}

// List lists all the volumes
func (r *Root) List() ([]volume.Volume, error) {
	r.m.Lock()
	defer r.m.Unlock()

	var ls []volume.Volume

	for _, v := range r.volumes {
		ls = append(ls, v)
	}

	return ls, nil
}

func (r *Root) Scope() string {
	return volume.LocalScope
}

func (r *Root) Remove(v volume.Volume) error {
	r.m.Lock()
	defer r.m.Unlock()

	lv, ok := v.(*Volume)
	if !ok {
		return errors.New("unknown volume type")
	}

	err := os.Remove(lv.hostDirectory)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to remove directory %s\n", lv.hostDirectory)
	}

	if lv.usedCount == 0 {
		delete(r.volumes, lv.name)
	}
	return nil
}

type Volume struct {
	m         sync.Mutex
	usedCount int
	// volume unique identifier
	name string
	// driverName is the name of the driver that created the volume.
	driverName string
	// The host directory where the nfs was mounted to
	hostDirectory string
	// name of the volume (src)
	source string
}

func (v *Volume) Name() string {
	return v.name
}

func (v *Volume) DriverName() string {
	return v.driverName
}

func (v *Volume) Path() string {
	return v.hostDirectory
}

func (v *Volume) Mount(id string) (string, error) {
	// The return value from this method will be passed to the container
	v.m.Lock()
	defer v.m.Unlock()

	// Even if Mount() fails, Unmount will be called.
	// So we increment usedCount ASAP to maintain the value
	// in a coherent way
	if err := v.use(); err != nil {
		return "", err
	}

	// retry=0,timeo=30: Fail if NFS server can't be reached in 30 second (no retries) - aggressive, but necessary because the Docker daemon becomes unresponsive if the mount command hangs.
	args := []string{"-o", "retry=0,timeo=30"}
	source := strings.Replace(v.source, "//", "://", 1)
	if err := libcontainer.DoMountCmd(v.DriverName(), source, v.hostDirectory, args); err != nil {
		return "", err
	}
	return v.hostDirectory, nil
}

func (v *Volume) Unmount(id string) error {
	v.m.Lock()
	defer v.m.Unlock()

	if err := v.release(); err != nil {
		return err
	}

	// Don't unmount if still being used
	if v.usedCount > 0 {
		return nil
	}

	err := exec.Command("umount", "-l", v.hostDirectory).Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to unmount nfs device %s from %s\n", v.Name(), v.hostDirectory)
		return err
	}
	return nil
}

func (v *Volume) Status() map[string]interface{} {
	return nil
}

func (v *Volume) use() error {
	// Note that the call to use() is assumed to be contained in a v.m.Lock()/Unlock() (the mutex isn't reentrant, so we can't lock it again here)
	v.usedCount++ // If Mount() fails, Unmount() (and therefore release()) will be called, so we need to increment usedCount even if the volume is already in use
	if v.usedCount > 1 {
		msg := fmt.Sprintf("The NFS volume '%s' is already being used by a running container in this Docker daemon", v.Name())
		logrus.Errorf(msg)
		return errors.New(msg)
	}
	return nil
}

func (v *Volume) release() error {
	// Note that the call to release() is assumed to be contained in a v.m.Lock()/Unlock() (the mutex isn't reentrant, so we can't lock it again here)
	if v.usedCount == 0 { // Shouldn't happen as long as Docker calls Mount()/Unmount() the way we think, but we've misunderstood the call sequence before
		msg := fmt.Sprintf("The NFS volume '%s' is being released more times than it has been used", v.Name())
		logrus.Errorf(msg)
		return errors.New(msg)
	}
	v.usedCount--
	return nil
}
