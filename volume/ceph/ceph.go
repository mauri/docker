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
	"syscall"

	"github.com/Sirupsen/logrus"
	"github.com/docker/docker/volume"
	"github.com/opencontainers/runc/libcontainer/utils"
)

const (
	CephImageSizeMB   = 1024 * 1024 // 1TB
	LuksDevMapperPath = "/dev/mapper/"
	cryptoLuksFsType  = "crypto_LUKS"
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

func (v *Volume) mapCephVolume() (error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := exec.Command("rbd", "map", v.name)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	var mappedDevicePath string
	if err := cmd.Run(); err == nil {
		mappedDevicePath = strings.TrimRight(stdout.String(), "\n")
		logrus.Infof("Succeeded in mapping Ceph volume '%s' to %s", v.name, mappedDevicePath)
		v.mappedDevicePath = mappedDevicePath
		return nil
	} else {
		msg := fmt.Sprintf("Failed to map Ceph volume '%s': %s - %s", v.name, err, strings.TrimRight(stderr.String(), "\n"))
		logrus.Errorf(msg)
		return errors.New(msg)
	}
}

func (r *Root) Remove(v volume.Volume) error {
	r.m.Lock()
	defer r.m.Unlock()
	delete(r.volumes, v.Name())
	return nil
}

func (v *Volume) unmapCephVolume() error {
	cmd := exec.Command("rbd", "unmap", v.mappedDevicePath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		logrus.Infof("Succeeded in unmapping Ceph volume '%s' from %s", v.name, v.mappedDevicePath)
		v.mappedDevicePath = ""
	} else {
		logrus.Errorf("Failed to unmap Ceph volume '%s' from %s: %s - %s", v.name, v.mappedDevicePath, err, strings.TrimRight(stderr.String(), "\n"))
	}
	return err
}

type Volume struct {
	m sync.Mutex
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
	// The RBD wrapper is the gatekeeper so no need to keep track of the usage

	// TODO: Might want to map with --options rw/ro here, but then we need to sneak in the RW flag somehow
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	cmd := exec.Command("rbd", "create", v.Name(), "--size", strconv.Itoa(CephImageSizeMB))
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		logrus.Infof("Created Ceph volume '%s'", v.Name())
	} else {
		// if rbd create returned EEXIST (17) the image is already there and we just need to map
		if exitError, ok := err.(*exec.ExitError); ok {
			imageSpec := strings.Split(v.Name(), "/") // strip the pool from the name
			imageName := imageSpec[len(imageSpec) - 1]
			waitStatus := exitError.Sys().(syscall.WaitStatus)
			if waitStatus.ExitStatus() == 17 || strings.Contains(stderr.String(), fmt.Sprintf("rbd image %s already exists", imageName)) {
				logrus.Infof("Found existing Ceph volume '%s'. %s", v.Name(), err)
			} else {
				msg := fmt.Sprintf("Failed to create Ceph volume '%s'. %s. (%d) ", v.Name(), stderr.String(), waitStatus.ExitStatus())
				logrus.Errorf(msg)
				return "", errors.New(msg)
			}
		} else {
			logrus.Errorf(fmt.Sprintf("Failed to get exit code from ceph volume creation '%s'. %s", v.Name(), stderr.String()))
			return "", err
		}
	}
	if err := v.mapCephVolume(); err != nil {
		return "", err
	}

	// Check that the volume already has a filesystem
	fsType, err := utils.DeviceHasFilesystem(v.mappedDevicePath)
	if err != nil {
		return "", err
	}

	deviceToMount := v.mappedDevicePath

	if fsType == cryptoLuksFsType {
		luksDevMapperName := getLuksDeviceMapperName(v.Name())
		cmd = exec.Command("cryptsetup", "luksOpen", "--allow-discards", "--key-file=-", deviceToMount, luksDevMapperName)
		stdin, err := cmd.StdinPipe()
		if err != nil {
			logrus.Errorf("Failed to luksOpen Ceph volume '%s' (device %s) - %s", v.Name(), deviceToMount, err)
			return "", err
		}
		defer stdin.Close()
		if err := cmd.Start(); err != nil {
			logrus.Errorf("Failed to luksOpen Ceph volume '%s' (device %s) - %s", v.Name(), deviceToMount, err)
			return "", err
		}

		key, err := getLuksKey(v.Name())
		if err != nil {
			logrus.Errorf("Failed to luksOpen Ceph volume '%s' (device %s) - %s", v.Name(), deviceToMount, err)
			return "", err
		}

		io.WriteString(stdin, key)
		stdin.Close()
		if err := cmd.Wait(); err != nil {
			logrus.Errorf("Failed to luksOpen Ceph volume '%s' (device %s) - %s", v.Name(), deviceToMount, err)
			return "", err
		}

		v.mappedLuksDevicePath = path.Join(LuksDevMapperPath, luksDevMapperName)
		deviceToMount = v.mappedLuksDevicePath

		fsType, err = utils.DeviceHasFilesystem(deviceToMount)
		logrus.Errorf("Filesystem in %s: %s", deviceToMount, fsType)
		if err != nil {
			return "", err
		}
	}

	if fsType == "" {
		cmd = exec.Command("mkfs.ext4", "-m0", "-E", "nodiscard,lazy_itable_init=0,lazy_journal_init=0,packed_meta_blocks=1", deviceToMount)
		logrus.Infof("Creating ext4 filesystem in newly created Ceph volume '%s' (device %s)", v.Name(), deviceToMount)
		if err := cmd.Run(); err != nil {
			logrus.Errorf("Failed to create ext4 filesystem in newly created Ceph volume '%s' (device %s) - %s", v.Name(), deviceToMount, err)
			return "", err
		}
	}

	fsckCmd, err := exec.Command("fsck", "-a", deviceToMount).Output()
	if err != nil {
		logrus.Errorf("Failed to check filesystem in %s - %s", v.Name(), err)
		return "", err
	}
	logrus.Infof("Checked filesystem in %s: %s", v.Name(), fsckCmd)

	// The return value from this method will be passed to the container
	return deviceToMount, nil
}

func (v *Volume) Unmount(id string) error {
	v.m.Lock()
	defer v.m.Unlock()
	defer v.unmapCephVolume()
	fsType, err := utils.DeviceHasFilesystem(v.mappedDevicePath)
	if err != nil {
		return err
	}

	if fsType == cryptoLuksFsType {
		luksDevMapperName := getLuksDeviceMapperName(v.Name())
		cmd := exec.Command("cryptsetup", "luksClose", luksDevMapperName)
		if err := cmd.Run(); err != nil {
			logrus.Errorf("Failed to luksClose Ceph volume '%s' (device %s) - %s", v.Name(), v.mappedDevicePath, err)
			return err
		}
	}
	return nil
}

func (v *Volume) Status() map[string]interface{} {
	return nil
}

func getLuksKey(name string) (string, error) {
	return name, nil
}

func getLuksDeviceMapperName(name string) (string) {
	return strings.Replace(name, "/", "--", -1)
}
