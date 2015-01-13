package daemon

import (
	"time"
	"path/filepath"
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

func (daemon *Daemon) ContainerRestore(job *engine.Job) engine.Status {
	if len(job.Args) != 2 {
		return job.Errorf("Usage: %s CONTAINER CHECKPOINT_ID", job.Name)
	}
	name := job.Args[0]

	container := daemon.Get(name)
	if container == nil {
		return job.Errorf("No such container: %s", name)
	}
	if err := container.Restore(job.Args[1]); err != nil {
		return job.Errorf("Cannot restore container %s: %s", name, err)
	}
	container.LogEvent("restore")
	return engine.StatusOK
}
