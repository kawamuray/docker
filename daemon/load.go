package daemon

import (
	"github.com/docker/docker/engine"
)

func (daemon *Daemon) ContainerLoad(job *engine.Job) engine.Status {
	if len(job.Args) != 2 || job.Args[0] == "" || job.Args[1] == "" {
		return job.Errorf("Usage: %s CONTAINER_ID NEW_IMAGE_ID", job.Name)
	}
	id := job.Args[0]

	if err := daemon.restoreSingleContainer(id); err != nil {
		return job.Error(err)
	}

	container := daemon.Get(id)
	container.ImageID = job.Args[1]
	// We also have to rebase the image id in configuration
	// to prevent depending non-existing image when migrated.
	container.Config.Image = job.Args[1]

	if err := daemon.createRootfs(container); err != nil {
		return job.Error(err)
	}
	return engine.StatusOK
}
