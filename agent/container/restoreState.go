package container

import (
	"time"

	"github.com/subutai-io/agent/db"
	"github.com/subutai-io/agent/lib/container"
	"github.com/subutai-io/agent/log"
)

func StateRestore() {
	for {
		doRestore()
		time.Sleep(time.Second * 30)
	}
}

func doRestore() {
	active := getContainersSupposedToBeRunning()

	for _, v := range active {
		if container.State(v.Name) != container.Running {
			log.Debug("Starting container " + v.Name)
			startErr := container.Start(v.Name)
			for i := 0; i < 5 && startErr != nil; i++ {
				log.Debug("Retrying container " + v.Name + " start")
				time.Sleep(time.Second * time.Duration(5+i))
				startErr = container.Start(v.Name)
			}
			if startErr != nil {
				log.Warn("Failed to start container " + v.Name + ": " + startErr.Error())
				v.State = container.Stopped
				db.SaveContainer(&v)
			}
		}
	}
}

func getContainersSupposedToBeRunning() []db.Container {
	list, err := db.FindContainers("", container.Running, "")

	if !log.Check(log.WarnLevel, "Getting list of running containers", err) {
		return list
	}

	return []db.Container{}
}
