package daemon

import (
	"os"
	"os/exec"
	"io/ioutil"
	"time"
	"fmt"
	"strings"
	"path/filepath"
	log "github.com/Sirupsen/logrus"
	"github.com/docker/docker/engine"
	"github.com/docker/docker/daemon/execdriver"
)

type ContainerCheckpoint struct {
	ID              string

	NetworkSettings *NetworkSettings
	CreatedAt       time.Time

	container       *Container
}

func (cp *ContainerCheckpoint) imagePath() string {
	return filepath.Join(cp.container.root, "checkpoints", cp.ID)
}

func (cp *ContainerCheckpoint) execdriverCheckpoint() *execdriver.Checkpoint {
	return &execdriver.Checkpoint{
		Command:   cp.container.command,
		ImagePath: cp.imagePath(),
		Volumes:   cp.container.Volumes,
	}
}

func (cp *ContainerCheckpoint) cleanFiles() {
	if err := os.RemoveAll(cp.imagePath()); err != nil {
		log.Warnf("failed to cleanup checkpoint image %s: %s", cp.imagePath(), err)
	}
}

func (cp *ContainerCheckpoint) clone(forContainer *Container) (*ContainerCheckpoint, error) {
	newCheckpoint := *cp
	networkSettings := *cp.NetworkSettings
	newCheckpoint.NetworkSettings = &networkSettings
	newCheckpoint.container = forContainer

	newImagePath := newCheckpoint.imagePath()
	if err := os.MkdirAll(newImagePath, 0775); err != nil {
		return nil, err
	}

	imagePath := cp.imagePath()
	dp, err := os.Open(imagePath)
	if err != nil {
		return nil, err
	}
	defer dp.Close()

	dirents, err := dp.Readdirnames(-1)
	if err != nil {
		return nil, err
	}
	for _, name := range dirents {
		// TODO solve this by better way
		if name == "restore.pid" {
			continue
		}
		src := filepath.Join(imagePath, name)
		dest := filepath.Join(newImagePath, name)
		// if err := os.Symlink(src, dest); err != nil {
		if err := os.Link(src, dest); err != nil {
			return nil, err
		}
	}
	return &newCheckpoint, nil
}

func (cp *ContainerCheckpoint) patchImage() error {
	imagePath := cp.imagePath()
	tmpdir, err := ioutil.TempDir(os.TempDir(), "docker-patchcriu-")
	if err != nil {
		return err
	}
	defer os.Remove(tmpdir) // No need to be RemoveAll, see below

	cmd := exec.Command("patch-criu", imagePath, tmpdir,
		"ip="+cp.container.NetworkSettings.IPAddress,
		"mac="+strings.Replace(cp.container.NetworkSettings.MacAddress, ":", "", -1))
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("patch-criu %s: output=%s", err, string(output))
	}

	dp, err := os.Open(tmpdir)
	if err != nil {
		return err
	}
	defer dp.Close()
	dirents, err := dp.Readdirnames(-1)
	if err != nil {
		return err
	}
	for _, name := range dirents {
		if err := os.Rename(filepath.Join(tmpdir, name), filepath.Join(imagePath, name)); err != nil {
			return err
		}
	}
	return nil
}

func (daemon *Daemon) ContainerCheckpoint(job *engine.Job) engine.Status {
	if len(job.Args) != 2 {
		return job.Errorf("Usage: %s CONTAINER", job.Name)
	}
	name := job.Args[0]
	container := daemon.Get(name)
	if container == nil {
		return job.Errorf("No such container: %s", name)
	}
	// TODO is this ok with job.Args[1] == "1"?
	if err := container.Checkpoint(job.Args[1] == "1"); err != nil {
		return job.Errorf("Cannot checkpoint container %s: %s", name, err)
	}
	container.LogEvent("checkpoint")
	return engine.StatusOK
}

func (daemon *Daemon) cloneContainer(container *Container) (*Container, error) {
	container.Lock()
	defer container.Unlock()

	configCopy := *container.Config
	configCopy.MacAddress = ""

	hostConfigCopy := *container.hostConfig
	clonedContainer, _, err := daemon.Create(&configCopy, &hostConfigCopy, "")
	if err != nil {
		return nil, fmt.Errorf("Failed to create cloned container of %s: %s", container.ID, err)
	}
	return clonedContainer, nil
}

func (daemon *Daemon) ContainerRestore(job *engine.Job) engine.Status {
	if len(job.Args) != 2 {
		return job.Errorf("Usage: %s CONTAINER CHECKPOINT_ID", job.Name)
	}
	name := job.Args[0]

	container := daemon.Get(name)
	if container == nil {
		return job.Errorf("No such container: %s", name)
	}

	checkpointID := job.Args[1]
	checkpoint := container.Checkpoints[checkpointID]
	if checkpoint == nil {
		return job.Errorf("No such checkpoint %s for container %s", checkpointID, container.ID)
	}

	clone := job.GetenvBool("clone")
	if clone {
		cloned, err := daemon.cloneContainer(container)
		if err != nil {
			return job.Errorf("%s", err)
		}
		container = cloned
		log.Infof("cloned container ID=%s", container.ID)
		checkpoint, err = checkpoint.clone(container)
		if err != nil {
			return job.Errorf("%s", err)
		}
	}

	if err := container.Restore(checkpoint, clone); err != nil {
		return job.Errorf("Cannot restore container %s: %s", name, err)
	}
	container.LogEvent("restore")
	return engine.StatusOK
}
