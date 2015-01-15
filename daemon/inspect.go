package daemon

import (
	"encoding/json"
	"fmt"

	"github.com/docker/docker/engine"
	"github.com/docker/docker/runconfig"
)

func (daemon *Daemon) ContainerInspect(job *engine.Job) engine.Status {
	if len(job.Args) != 1 {
		return job.Errorf("usage: %s NAME", job.Name)
	}
	name := job.Args[0]
	if container := daemon.Get(name); container != nil {
		container.Lock()
		defer container.Unlock()
		if job.GetenvBool("raw") {
			b, err := json.Marshal(&struct {
				*Container
				HostConfig *runconfig.HostConfig
			}{container, container.hostConfig})
			if err != nil {
				return job.Error(err)
			}
			job.Stdout.Write(b)
			return engine.StatusOK
		}

		out := &engine.Env{}
		out.SetJson("Id", container.ID)
		out.SetAuto("Created", container.Created)
		out.SetJson("Path", container.Path)
		out.SetList("Args", container.Args)
		out.SetJson("Config", container.Config)
		out.SetJson("State", container.State)
		out.Set("Image", container.ImageID)
		out.SetJson("NetworkSettings", container.NetworkSettings)
		out.Set("ResolvConfPath", container.ResolvConfPath)
		out.Set("HostnamePath", container.HostnamePath)
		out.Set("HostsPath", container.HostsPath)
		out.SetJson("Name", container.Name)
		out.SetInt("RestartCount", container.RestartCount)
		out.Set("Driver", container.Driver)
		out.Set("ExecDriver", container.ExecDriver)
		out.Set("MountLabel", container.MountLabel)
		out.Set("ProcessLabel", container.ProcessLabel)
		out.SetJson("Volumes", container.Volumes)
		out.SetJson("VolumesRW", container.VolumesRW)
		out.SetJson("AppArmorProfile", container.AppArmorProfile)

		out.SetList("ExecIDs", container.GetExecIDs())

		if children, err := daemon.Children(container.Name); err == nil {
			for linkAlias, child := range children {
				container.hostConfig.Links = append(container.hostConfig.Links, fmt.Sprintf("%s:%s", child.Name, linkAlias))
			}
		}

		out.SetJson("HostConfig", container.hostConfig)

		checkpoints := make([]*ContainerCheckpoint, 0, len(container.Checkpoints))
		// Make checkpoint list with ordering by creation time
		for _, checkpoint := range container.Checkpoints {
			checkpoints = append(checkpoints, checkpoint)
			for i := len(checkpoints)-1; i > 0; i-- {
				if checkpoints[i-1].CreatedAt.Before(checkpoint.CreatedAt) {
					break
				}
				checkpoints[i], checkpoints[i-1] = checkpoints[i-1], checkpoint
			}
		}
		out.SetJson("Checkpoints", checkpoints)

		container.hostConfig.Links = nil
		if _, err := out.WriteTo(job.Stdout); err != nil {
			return job.Error(err)
		}
		return engine.StatusOK
	}
	return job.Errorf("No such container: %s", name)
}

func (daemon *Daemon) ContainerExecInspect(job *engine.Job) engine.Status {
	if len(job.Args) != 1 {
		return job.Errorf("usage: %s ID", job.Name)
	}
	id := job.Args[0]
	eConfig, err := daemon.getExecConfig(id)
	if err != nil {
		return job.Error(err)
	}

	b, err := json.Marshal(*eConfig)
	if err != nil {
		return job.Error(err)
	}
	job.Stdout.Write(b)
	return engine.StatusOK
}
