package cli

import (
	"strings"
	"github.com/subutai-io/agent/lib/fs"
	container2 "github.com/subutai-io/agent/lib/container"
	"fmt"
	"github.com/subutai-io/agent/log"
	"github.com/subutai-io/agent/config"
	"path"
	"os"
	"path/filepath"
)

func CreateSnapshot(container, partition, label string, stopContainer bool) {

	container = strings.TrimSpace(container)
	partition = strings.ToLower(strings.TrimSpace(partition))
	label = strings.ToLower(strings.TrimSpace(label))

	checkArgument(container != "", "Invalid container name")

	checkPartitionName(partition)

	checkArgument(label != "", "Invalid snapshot label")

	// check that container exists
	checkState(container2.IsContainer(container), "Container %s not found", container)
	// check that snapshot with such label does not exist
	snapshot := getSnapshotName(container, partition, label)
	checkState(!fs.DatasetExists(snapshot), "Snapshot %s already exists", snapshot)

	if stopContainer {
		if container2.State(container) == container2.Running {
			LxcStop(container)
			defer LxcStart(container)
		}
	}

	// create snapshot
	err := fs.CreateSnapshot(snapshot, partition == "all")
	checkCondition(err == nil, func() {
		log.Error("Failed to create snapshot ", err.Error())
	})
}

func RemoveSnapshot(container, partition, label string) {
	container = strings.TrimSpace(container)
	partition = strings.ToLower(strings.TrimSpace(partition))
	label = strings.ToLower(strings.TrimSpace(label))

	checkArgument(container != "", "Invalid container name")

	checkPartitionName(partition)

	checkArgument(label != "", "Invalid snapshot label")

	// check that container exists
	checkState(container2.IsContainer(container), "Container %s not found", container)
	// check that snapshot with such label exists
	snapshot := getSnapshotName(container, partition, label)
	//checkState(fs.DatasetExists(snapshot), "Snapshot %s does not exist", snapshot)

	err := fs.RemoveDataset(snapshot, partition == "all")
	checkCondition(err == nil, func() {
		log.Error("Failed to remove snapshot ", err.Error())
	})
}

func ListSnapshots(container, partition string) string {
	container = strings.TrimSpace(container)
	partition = strings.ToLower(strings.TrimSpace(partition))

	if partition != "" {
		//check that container is specified if partition is present
		checkArgument(container != "", "Please, specify container name")
	}

	if container != "" {
		// check that container exists
		checkState(container2.IsContainer(container), "Container %s not found", container)
	}

	if partition != "" {
		checkPartitionName(partition)
	}

	var out string
	var err error
	if container == "" {
		//list snapshots of all containers
		out, err = fs.ListSnapshots("")
		//remove lines belonging to templates
		if err == nil {
			lines := strings.Split(out, "\n")
			templates := container2.Templates()
			out = ""
			for _, line := range lines {
				found := false
				for _, template := range templates {
					if strings.Contains(line, path.Join(config.Agent.Dataset, template)+"/") {
						found = true
						break
					}
				}
				if !found {
					out += line + "\n"
				}
			}
		}

	} else {
		if partition != "" {
			out, err = fs.ListSnapshots(getSnapshotName(container, partition, ""))
		} else {
			out, err = fs.ListSnapshots(container)
		}
	}

	checkCondition(err == nil, func() {
		log.Error("Failed to list snapshots ", err.Error())
	})

	out = strings.TrimRight(out, "\n")

	return out
}

func RollbackToSnapshot(container, partition, label string, forceRollback, stopContainer bool) {
	container = strings.TrimSpace(container)
	partition = strings.ToLower(strings.TrimSpace(partition))
	label = strings.ToLower(strings.TrimSpace(label))

	checkArgument(container != "", "Invalid container name")

	checkPartitionName(partition)

	checkArgument(label != "", "Invalid snapshot label")

	// check that container exists
	checkState(container2.IsContainer(container), "Container %s not found", container)
	// check that snapshot with such label exists
	snapshot := getSnapshotName(container, partition, label)
	checkCondition(fs.DatasetExists(snapshot), func() {
		if partition != "all" {
			log.Error(fmt.Sprintf("Snapshot %s does not exist", snapshot))
		} else {
			for _, part := range fs.ChildDatasets {
				snap := getSnapshotName(container, part, label)
				checkState(fs.DatasetExists(snap), "Snapshot %s does not exist", snap)
			}
		}
	})

	if stopContainer {
		if container2.State(container) == container2.Running {
			LxcStop(container)
			defer LxcStart(container)
		}
	}

	if partition == "all" {
		//perform recursive rollback
		out, err := fs.ListSnapshotNamesOnly(container)
		checkCondition(err == nil, func() {
			log.Error("Failed to list snapshots", err.Error())

		})

		//rollback to snapshots
		snapshots := strings.Split(out, "\n")
		for _, snapshot := range snapshots {
			snapshot = strings.TrimSpace(strings.TrimPrefix(snapshot, config.Agent.Dataset))
			if snapshot != "" && strings.HasSuffix(snapshot, "@"+label) {
				err = fs.RollbackToSnapshot(snapshot, forceRollback)
				checkCondition(err == nil, func() {
					errString := err.Error()
					if strings.Contains(errString, "use '-r' to force deletion") {
						errString = strings.Replace(errString, "use '-r' to force deletion", "use '-f' to force deletion", 1)
					}
					log.Error("Failed to rollback to snapshot ", errString)
				})
			}
		}

	} else {

		err := fs.RollbackToSnapshot(snapshot, forceRollback)
		checkCondition(err == nil, func() {
			errString := err.Error()
			if strings.Contains(errString, "use '-r' to force deletion") {
				errString = strings.Replace(errString, "use '-r' to force deletion", "use '-f' to force deletion", 1)
			}
			log.Error("Failed to rollback to snapshot ", errString)
		})
	}

}

func SendContainerSnapshots(container, destDir string, labels ... string) {
	container = strings.TrimSpace(container)
	checkArgument(container != "", "Invalid container name")
	checkState(container2.IsContainer(container), "Container %s not found", container)

	destDir = strings.TrimSpace(destDir)
	checkArgument(destDir != "", "Invalid destination directory")
	checkState(fs.FileExists(destDir), "Destination directory %s not found", destDir)

	checkArgument(len(labels) == 1 || len(labels) == 2, "Invalid number of snapshot labels")
	for _, label := range labels {
		checkArgument(label != "", "Invalid snapshot label")
	}
	for _, label := range labels {
		for _, partition := range fs.ChildDatasets {

			// check that snapshot with such label exists
			snapshot := getSnapshotName(container, partition, label)
			checkState(fs.DatasetExists(snapshot), "Snapshot %s does not exist", snapshot)
		}
	}

	// create dump file
	parts := []string{container}
	parts = append(parts, labels...)
	targetDir := path.Join(destDir, strings.Join(parts, "_"))
	os.MkdirAll(targetDir, 0755)

	parent := container2.GetProperty(container, "subutai.parent")
	parentOwner := container2.GetProperty(container, "subutai.parent.owner")
	parentVersion := container2.GetProperty(container, "subutai.parent.version")
	parentRef := strings.Join([]string{parent, parentOwner, parentVersion}, ":")

	for _, partition := range fs.ChildDatasets {
		var err error
		if len(labels) == 1 {

			// send incremental delta between parent and child to delta file
			err = fs.SendStream(getSnapshotName(parentRef, partition, "now"),
				getSnapshotName(container, partition, labels[0]), path.Join(targetDir, partition+".delta"))
		} else {
			// send incremental delta between two child snapshots to delta file
			err = fs.SendStream(getSnapshotName(container, partition, labels[0]),
				getSnapshotName(container, partition, labels[1]), path.Join(targetDir, partition+".delta"))
		}

		log.Check(log.ErrorLevel, "Sending stream for partition "+partition, err)

	}

	//copy config file
	log.Check(log.ErrorLevel, "Copying config file", fs.Copy(path.Join(config.Agent.LxcPrefix, container, "config"), path.Join(targetDir, "config")))

	//archive template contents
	targetFile := targetDir + ".tar.gz"
	fs.Compress(targetDir, targetFile)
	log.Check(log.WarnLevel, "Removing temporary directory", os.RemoveAll(targetDir))
	log.Info(container + " snapshots got dumped to " + targetFile)
}

func ReceiveContainerSnapshots(container, sourceFile string) {
	container = strings.TrimSpace(container)
	checkArgument(container != "", "Invalid container name")

	sourceFile = strings.TrimSpace(sourceFile)
	checkArgument(sourceFile != "", "Invalid path to snapshots file")
	checkCondition(fs.FileExists(sourceFile), func() {
		checkState(fs.FileExists(path.Join(config.Agent.CacheDir, sourceFile)), "File %s not found", sourceFile)
		sourceFile = path.Join(config.Agent.CacheDir, sourceFile)
	})

	//extract archive file
	dest := path.Join(config.Agent.CacheDir, getFileName(sourceFile))
	log.Check(log.ErrorLevel, "Decompressing snapshots file", fs.Decompress(sourceFile, dest))

	//check presence of all deltas
	for _, partition := range fs.ChildDatasets {
		checkState(fs.FileExists(path.Join(dest, partition+".delta")), "Snapshot file for partition %s not found", partition)
	}
	//check presence of config file
	checkState(fs.FileExists(path.Join(dest, "config")), "Config file not found")

	//precreate parent dataset if not exists
	if !fs.DatasetExists(container) {
		fs.CreateDataset(container)
	}

	//receive snapshots
	for _, partition := range fs.ChildDatasets {

		err := fs.ReceiveStream(path.Join(container, partition), path.Join(dest, partition+".delta"), true)
		log.Check(log.ErrorLevel, "Receiving snapshots for partition "+partition, err)

	}

	//copy config file
	log.Check(log.ErrorLevel, "Copying config file", fs.Copy(path.Join(dest, "config"), path.Join(config.Agent.LxcPrefix, container, "config")))

	//remove decompressed archive folder
	log.Check(log.WarnLevel, "Removing temporary directory", os.RemoveAll(dest))
}

func getFileName(filePath string) string {
	file := filepath.Base(filePath)
	for i := 0; i < 3; i++ {
		file = strings.TrimSuffix(file, filepath.Ext(file))
	}
	return file
}

func getSnapshotName(container, partition, label string) string {
	if label == "" {
		if partition == "config" {
			return fmt.Sprintf("%s", container)
		} else {
			return fmt.Sprintf("%s/%s", container, partition)
		}
	} else {
		if partition == "config" || partition == "all" {
			return fmt.Sprintf("%s@%s", container, label)
		} else {
			return fmt.Sprintf("%s/%s@%s", container, partition, label)
		}
	}
}

func checkPartitionName(partition string) {
	checkArgument(partition != "", "Invalid container partition")
	partitionFound := false
	for _, vol := range fs.ChildDatasets {
		if vol == partition {
			partitionFound = true
			break
		}
	}

	if partition == "config" || partition == "all" {
		partitionFound = true
	}
	checkArgument(partitionFound, "Invalid partition %s", partition)
}
