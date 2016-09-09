package nfsvolumedriver

import (
	"errors"
	"fmt"
	"sync"

	"github.com/docker/docker/volume"
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

func (r *Root) Create(name string, _ map[string]string) (volume.Volume, error) {
	r.m.Lock()
	defer r.m.Unlock()

	v, exists := r.volumes[name]
	if !exists {
		v = &Volume{
			driverName: r.Name(),
			name:       name,
		}
		r.volumes[name] = v
	}
	v.use()
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

func (r *Root) Remove(v volume.Volume) error {
	r.m.Lock()
	defer r.m.Unlock()

	lv, ok := v.(*Volume)
	if !ok {
		return errors.New("unknown volume type")
	}
	lv.release()
	if lv.usedCount == 0 {
		delete(r.volumes, lv.name)
	}
	return nil
}

type Volume struct {
	m         sync.Mutex
	usedCount int
	// unique name of the volume
	name string
	// driverName is the name of the driver that created the volume.
	driverName string
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

func (v *Volume) Mount(id string) (string, error) {
	// The return value from this method will be passed to the container
	return v.name, nil
}

func (v *Volume) Unmount(id string) error {
	return nil
}

func (v *Volume) Status() map[string]interface{} {
	return nil
}

func (v *Volume) use() {
	v.m.Lock()
	v.usedCount++
	v.m.Unlock()
}

func (v *Volume) release() {
	v.m.Lock()
	v.usedCount--
	v.m.Unlock()
}
