package cephvolumedriver

import (
	"errors"
	"sync"

	"bytes"
	"fmt"
	"github.com/Sirupsen/logrus"
	"github.com/docker/docker/volume"
	"os/exec"
	"strconv"
	"strings"
)

const CephImageSizeMB = 1024 * 1024 // 1TB

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
			driverName:       r.Name(),
			name:             name,
			mappedDevicePath: "", // Will be set by Mount()
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

func (v *Volume) Mount() (mappedDevicePath string, returnedError error) {
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
	if err := cmd.Run(); err == nil {
		logrus.Infof("Created Ceph volume '%s'", v.Name())
		v.mappedDevicePath, err = mapCephVolume(v.Name())
		if err != nil {
			return "", err
		}
		cmd = exec.Command("mkfs.ext4", "-m0", v.mappedDevicePath)
		logrus.Infof("Creating ext4 filesystem in newly created Ceph volume '%s' (device %s)", v.Name(), v.mappedDevicePath)
		if err := cmd.Run(); err != nil {
			logrus.Errorf("Failed to create ext4 filesystem in newly created Ceph volume '%s' (device %s) - %s", v.Name(), v.mappedDevicePath, err)
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

	// The return value from this method will be passed to the container
	return v.mappedDevicePath, nil
}

func (v *Volume) Unmount() error {
	v.m.Lock()
	defer v.m.Unlock()

	if err := v.release(); err != nil {
		return err
	}
	//if v.usedCount == 0 { // Even if the volume is attempted to be used multiple times, only the first use will actually succeed in mapping it
		unmapCephVolume(v.name, v.mappedDevicePath)
	//}

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
