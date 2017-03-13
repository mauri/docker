package cephvolumedriver

import (
	"errors"
	"io"
	"path"
	"sync"

	"bytes"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/Sirupsen/logrus"
	"github.com/docker/docker/volume"
	"github.com/opencontainers/runc/libcontainer/utils"
)

const (
	CephImageSizeMB   = 1024 * 1024 // 1TB
	LuksDevMapperPath = "/dev/mapper/"
	criptoLUKS        = "crypto_LUKS"
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
	return "ceph"
}

func (r *Root) Create(name string, _ map[string]string) (volume.Volume, error) {
	r.m.Lock()
	defer r.m.Unlock()

	v, exists := r.volumes[name]
	if !exists {
		v = &Volume{
			driverName:           r.Name(),
			name:                 name,
			mappedDevicePath:     "", // Will be set by Mount()
			mappedLuksDevicePath: "", // Will be set by Mount()
		}
		r.volumes[name] = v
	}

	return v, nil
}

// Get looks up the volume for the given name and returns it if found
func (r *Root) Get(name string) (volume.Volume, error) {
	r.m.Lock()
	v, exists := r.volumes[name]
	r.m.Unlock()
	if !exists {
		return nil, fmt.Errorf("volume not found")

	}
	return v, nil
}

// List lists all the volumes
func (r *Root) List() ([]volume.Volume, error) {
	var ls []volume.Volume
	r.m.Lock()
	for _, v := range r.volumes {
		ls = append(ls, v)
	}
	r.m.Unlock()
	return ls, nil
}

func (r *Root) Scope() string {
	return volume.LocalScope
}

func mapCephVolume(name string) (string, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := exec.Command("rbd", "map", name)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	var mappedDevicePath string
	if err := cmd.Run(); err == nil {
		mappedDevicePath = strings.TrimRight(stdout.String(), "\n")
		logrus.Infof("Succeeded in mapping Ceph volume '%s' to %s", name, mappedDevicePath)
		return mappedDevicePath, nil
	} else {
		msg := fmt.Sprintf("Failed to map Ceph volume '%s': %s - %s", name, err, strings.TrimRight(stderr.String(), "\n"))
		logrus.Errorf(msg)
		return "", errors.New(msg)
	}
}

func (r *Root) Remove(v volume.Volume) error {
	r.m.Lock()
	defer r.m.Unlock()
	delete(r.volumes, v.Name())
	return nil
}

func unmapCephVolume(name, mappedDevicePath string) error {
	cmd := exec.Command("rbd", "unmap", mappedDevicePath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		logrus.Infof("Succeeded in unmapping Ceph volume '%s' from %s", name, mappedDevicePath)
	} else {
		logrus.Errorf("Failed to unmap Ceph volume '%s' from %s: %s - %s", name, mappedDevicePath, err, strings.TrimRight(stderr.String(), "\n"))
	}
	return err
}

type Volume struct {
	m         sync.Mutex
	usedCount int
	// unique name of the volume
	name string
	// driverName is the name of the driver that created the volume.
	driverName string
	// the path to the device to which the Ceph volume has been mapped
	mappedDevicePath string
	// the path to the LUKS folder to which the Ceph device has been mapped
	mappedLuksDevicePath string
}

func (v *Volume) Name() string {
	return v.name
}

func (v *Volume) DriverName() string {
	return v.driverName
}

func (v *Volume) Path() string {
	return ""
}

func (v *Volume) Mount(id string) (mappedDevicePath string, returnedError error) {
	v.m.Lock()
	defer v.m.Unlock()
	logrus.Debugf("Mount: %s", mappedDevicePath)
	// Note that if Mount() returns an error, Docker will still call Unmount(), so there is no need to call release() if anything fails
	if err := v.use(); err != nil {
		return "", err
	}

	// TODO: Might want to map with --options rw/ro here, but then we need to sneak in the RW flag somehow
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	cmd := exec.Command("rbd", "create", v.Name(), "--size", strconv.Itoa(CephImageSizeMB))
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		logrus.Infof("Created Ceph volume '%s'", v.Name())
		v.mappedDevicePath, err = mapCephVolume(v.Name())
		if err != nil {
			return "", err
		}
	} else if strings.Contains(stderr.String(), fmt.Sprintf("rbd image %s already exists", v.Name())) {
		logrus.Infof("Found existing Ceph volume %s", v.Name())
		v.mappedDevicePath, err = mapCephVolume(v.Name())
		if err != nil {
			return "", err
		}
	} else {
		msg := fmt.Sprintf("Failed to create Ceph volume '%s' - %s", v.Name(), err)
		logrus.Errorf(msg)
		return "", errors.New(msg)
	}

	// Check that the volume already has a filesystem
	fsType, err := utils.DeviceHasFilesystem(v.mappedDevicePath)
	if err != nil {
		return "", err
	}

	if fsType == criptoLUKS {
		cmd = exec.Command("cryptsetup", "luksOpen", v.mappedDevicePath, v.Name())
		stdin, err := cmd.StdinPipe()
		if err != nil {
			logrus.Errorf("Failed to luksOpen Ceph volume '%s' (device %s) - %s", v.Name(), v.mappedDevicePath, err)
			return "", err
		}
		defer stdin.Close()
		if err := cmd.Start(); err != nil {
			logrus.Errorf("Failed to luksOpen Ceph volume '%s' (device %s) - %s", v.Name(), v.mappedDevicePath, err)
			return "", err
		}

		key, err := getLuksKey(v.Name())
		if err != nil {
			logrus.Errorf("Failed to luksOpen Ceph volume '%s' (device %s) - %s", v.Name(), v.mappedDevicePath, err)
			return "", err
		}

		io.WriteString(stdin, key)
		io.WriteString(stdin, "\n")
		if err := cmd.Wait(); err != nil {
			logrus.Errorf("Failed to luksOpen Ceph volume '%s' (device %s) - %s", v.Name(), v.mappedDevicePath, err)
			return "", err
		}
		v.mappedLuksDevicePath = path.Join(LuksDevMapperPath, v.Name())
	}

	if fsType == "" {
		//if fsType == "" {
		cmd = exec.Command("mkfs.ext4", "-m0", "-E", "nodiscard,lazy_itable_init=0,lazy_journal_init=0,packed_meta_blocks=1", v.mappedDevicePath)
		logrus.Infof("Creating ext4 filesystem in newly created Ceph volume '%s' (device %s)", v.Name(), v.mappedDevicePath)
		if err := cmd.Run(); err != nil {
			logrus.Errorf("Failed to create ext4 filesystem in newly created Ceph volume '%s' (device %s) - %s", v.Name(), v.mappedDevicePath, err)
			return "", err
		}
	}

	fsckCmd, err := exec.Command("fsck", "-a", v.mappedDevicePath).Output()
	if err != nil {
		logrus.Errorf("Failed to check filesystem in %s - %s", v.Name(), err)
		return "", err
	}
	logrus.Infof("Checked filesystem in %s: %s", v.Name(), fsckCmd)

	if fsType == criptoLUKS {
		return v.mappedLuksDevicePath, nil
	}

	// The return value from this method will be passed to the container
	return v.mappedDevicePath, nil
}

func (v *Volume) Unmount(id string) error {
	v.m.Lock()
	defer v.m.Unlock()

	if err := v.release(); err != nil {
		return err
	}

	fsType, err := utils.DeviceHasFilesystem(v.mappedDevicePath)
	if err != nil {
		return err
	}

	if fsType == "crypto_LUKS" {
		cmd := exec.Command("cryptsetup", "luksClose", v.Name())
		if err := cmd.Run(); err != nil {
			logrus.Errorf("Failed to luksClose Ceph volume '%s' (device %s) - %s", v.Name(), v.mappedDevicePath, err)
			return err
		}
	}
	//if v.usedCount == 0 { // Even if the volume is attempted to be used multiple times, only the first use will actually succeed in mapping it
	unmapCephVolume(v.name, v.mappedDevicePath)
	//}

	return nil
}

func (v *Volume) Status() map[string]interface{} {
	return nil
}

func (v *Volume) use() error {
	// Note that the call to use() is assumed to be contained in a v.m.Lock()/Unlock() (the mutex isn't reentrant, so we can't lock it again here)
	v.usedCount++ // If Mount() fails, Unmount() (and therefore release()) will be called, so we need to increment usedCount even if the volume is already in use
	if v.usedCount > 1 {
		msg := fmt.Sprintf("The Ceph volume '%s' is already being used by a running container in this Docker daemon", v.Name())
		logrus.Errorf(msg)
		return errors.New(msg)
	}
	return nil
}

func (v *Volume) release() error {
	// Note that the call to release() is assumed to be contained in a v.m.Lock()/Unlock() (the mutex isn't reentrant, so we can't lock it again here)
	if v.usedCount == 0 { // Shouldn't happen as long as Docker calls Mount()/Unmount() the way we think, but we've misunderstood the call sequence before
		msg := fmt.Sprintf("Bug: The Ceph volume '%s' is being released more times than it has been used", v.Name())
		logrus.Errorf(msg)
		return errors.New(msg)
	}
	v.usedCount--
	return nil
}

func getLuksKey(name string) (string, error) {
	return name, nil
}
