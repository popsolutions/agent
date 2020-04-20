package cli

import (
	"encoding/json"
	"fmt"
	"github.com/cavaliercoder/grab"
	"github.com/nightlyone/lockfile"
	"github.com/pkg/errors"
	"github.com/subutai-io/agent/agent/util"
	"github.com/subutai-io/agent/config"
	"github.com/subutai-io/agent/db"
	"github.com/subutai-io/agent/lib/common"
	"github.com/subutai-io/agent/lib/container"
	"github.com/subutai-io/agent/lib/exec"
	"github.com/subutai-io/agent/lib/fs"
	"github.com/subutai-io/agent/lib/gpg"
	"github.com/subutai-io/agent/lib/net"
	"github.com/subutai-io/agent/log"
	"gopkg.in/cheggaaa/pb.v1"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const maxDownloadAttempts = 3

const wrappedTemplateSuffix = ".tar.gz"
const Md5DigestMethod = "md5"
const Sha256DigestMethod = "sha256"

type Template struct {
	Id           string `json:"id"`
	Name         string `json:"name"`
	Owner        string `json:"owner"`
	Version      string `json:"version"`
	MD5          string `json:"md5"`
	DigestMethod string `json:"digest-method"`
	DigestHash   string `json:"digest"`
	Parent       string `json:"parent"`
	Size         int64  `json:"size"`
	FullRef      string `json:"full-ref"`
	PrefSize     string `json:"pref-size"`
}

func init() {
	if _, err := os.Stat(config.Agent.CacheDir); os.IsNotExist(err) {
		os.MkdirAll(config.Agent.CacheDir, 0755)
	}
}

// getTemplateInfoById retrieves template name from global repository by passed id string
func getTemplateInfoById(t *Template, id string) {
	theUrl := config.CdnUrl + "/template?id=" + id

	clnt := util.GetClient(config.CDN.AllowInsecure, 30)

	response, err := util.RetryGet(theUrl, clnt, 3)

	log.Check(log.ErrorLevel, "Retrieving template info, get: "+theUrl, err)
	defer util.Close(response)

	if response.StatusCode == 404 {
		log.Error("Template " + t.Name + " not found")
	}
	if response.StatusCode != 200 {
		log.Error("Failed to get template info:  " + response.Status)
	}

	body, err := ioutil.ReadAll(response.Body)
	log.Check(log.ErrorLevel, "Reading template info", err)

	var templ Template
	if log.Check(log.WarnLevel, "Parsing response body", json.Unmarshal(body, &templ)) {
		log.Error("Failed to parse template info")
	}

	t.Name = templ.Name
	t.Owner = templ.Owner
	t.Version = templ.Version
	t.Id = templ.Id
	t.MD5 = templ.MD5
	t.Parent = templ.Parent
	t.Size = templ.Size
	t.DigestMethod = templ.DigestMethod
	t.DigestHash = templ.DigestHash

	log.Debug("Template identified as " + t.Name + "@" + t.Owner + ":" + t.Version)
}

//TODO extract all BZR CDN related functionality to own package
//TODO urlEncode the url
func getTemplateInfoByName(t *Template, name string, owner string, version string) {
	theUrl := config.CdnUrl + "/template?name=" + name

	if owner != "" {
		theUrl += "&owner=" + owner
	}

	if version == "" {
		theUrl += "&version=latest"
	} else {
		theUrl += "&version=" + version
	}

	clnt := util.GetClient(config.CDN.AllowInsecure, 30)

	response, err := util.RetryGet(theUrl, clnt, 3)

	log.Check(log.ErrorLevel, "Retrieving template info, get: "+theUrl, err)
	defer util.Close(response)

	if response.StatusCode == 404 {
		log.Error("Template " + t.Name + " not found")
	}
	if response.StatusCode != 200 {
		log.Error("Failed to get template info:  " + response.Status)
	}

	body, err := ioutil.ReadAll(response.Body)
	log.Check(log.ErrorLevel, "Reading template info", err)

	var templ Template
	if log.Check(log.WarnLevel, "Parsing response body", json.Unmarshal(body, &templ)) {
		log.Error("Failed to parse template info")
	}

	t.Name = templ.Name
	t.Owner = templ.Owner
	t.Version = templ.Version
	t.Id = templ.Id
	t.MD5 = templ.MD5
	t.Parent = templ.Parent
	t.Size = templ.Size
	t.DigestMethod = templ.DigestMethod
	t.DigestHash = templ.DigestHash

	log.Debug("Template identified as " + t.Name + "@" + t.Owner + ":" + t.Version)
}

func getTemplateInfo(template string) Template {

	var t Template

	if id := strings.Split(template, "id:"); len(id) > 1 {
		templateId := id[1]

		getTemplateInfoById(&t, templateId)

	} else {

		// full template reference is template@owner:version e.g. master@subutai:4.0.0
		// if owner is missing then we use verified only, if version is missing we use latest version

		if templateNameNOwnerNVersionRx.MatchString(template) {
			groups := util.MatchRegexGroups(templateNameNOwnerNVersionRx, template)

			getTemplateInfoByName(&t, groups["name"], groups["owner"], groups["version"])
		} else if templateNameNOwnerRx.MatchString(template) {
			groups := util.MatchRegexGroups(templateNameNOwnerRx, template)

			getTemplateInfoByName(&t, groups["name"], groups["owner"], "")
		} else if templateNameRx.MatchString(template) {
			groups := util.MatchRegexGroups(templateNameRx, template)

			getTemplateInfoByName(&t, groups["name"], "", "")
		} else {
			log.Error("Invalid template name " + template)
		}

	}

	return t
}

// md5sum returns MD5 hash sum of specified file
func md5sum(filePath string) string {
	hash, err := fs.Md5Sum(filePath)
	log.Check(log.WarnLevel, "Getting md5sum of "+filePath, err)
	return hash
}

func sha256(filePath string) string {
	hash, err := fs.Sha256Sum(filePath)
	log.Check(log.WarnLevel, "Getting sha256sum of "+filePath, err)
	return hash
}

func verifyChecksum(template Template, filePath string) bool {
	if template.DigestMethod == Sha256DigestMethod {
		return template.DigestHash == sha256(filePath)
	} else if template.DigestMethod == Md5DigestMethod {
		return template.DigestHash == md5sum(filePath)
	}

	return false
}

func LxcImport(name, token string, auxDepList ...string) {
	var err error

	if !fs.DatasetExists("") {
		log.Fatal("Root dataset " + config.Agent.Dataset + " not mounted")
	}

	if container.LxcInstanceExists(name) && name == container.Management && len(token) > 1 {
		gpg.ExchangeAndEncrypt(container.Management, token)
		return
	}

	var t Template
	var templateRef string
	var localArchive string

	local := fs.FileExists(name)

	if !local {
		t = getTemplateInfo(name)
		templateRef = strings.Join([]string{t.Name, t.Owner, t.Version}, ":")
		localArchive = path.Join(config.Agent.CacheDir, t.Id)
	} else {
		//for local import we accept full path to template archive
		if !fs.FileExists(name) {
			log.Error("Template " + name + " not found")
		}

		t.Name = filepath.Base(name)
		templateRef = "tmpl_" + t.Name
		localArchive = name
	}

	log.Info("Importing " + t.Name)

	var lock lockfile.Lockfile
	for lock, err = common.LockFile(templateRef, "import"); err != nil; lock, err = common.LockFile(templateRef, "import") {
		time.Sleep(time.Second * 1)
	}
	defer lock.Unlock()

	//for local import this check currently does not work
	if container.LxcInstanceExists(templateRef) {
		if t.Name == container.Management && !container.IsContainer(container.Management) {
			initManagement(templateRef)
			return
		}
		//!important used by Console
		log.Info(t.Name + " instance exists")
		return
	}

	var archiveExists = fs.FileExists(localArchive)

	if archiveExists {

		log.Debug("Template archive is present in local cache")

		if !local {
			if verifyChecksum(t, localArchive) {

				log.Debug("File integrity is verified")
			} else {

				//make agent re-download verified template from CDN
				archiveExists = false
			}
		} else {
			log.Warn("Skipping file integrity verification since --local flag was passed")
		}

	} else {

		log.Debug("Template archive is missing in local cache")
	}

	if !archiveExists {
		download(t)
	}

	//!important used by Console
	log.Info("Unpacking template " + t.Name)
	log.Debug(localArchive + " to " + templateRef)
	extractDir := path.Join(config.Agent.CacheDir, templateRef)
	log.Check(log.FatalLevel, "Extracting tgz", fs.Decompress(localArchive, extractDir))

	templateName := container.GetConfigItem(extractDir+"/config", "subutai.template")
	templateOwner := container.GetConfigItem(extractDir+"/config", "subutai.template.owner")
	templateVersion := container.GetConfigItem(extractDir+"/config", "subutai.template.version")

	if local {
		//rename template directory to follow full reference convention
		templateRef = strings.Join([]string{templateName, templateOwner, templateVersion}, ":")
		log.Check(log.ErrorLevel, "Renaming template", os.Rename(extractDir, path.Join(config.Agent.CacheDir, templateRef)))
		extractDir = path.Join(config.Agent.CacheDir, templateRef)
	}

	if container.IsTemplate(templateRef) {
		log.Check(log.WarnLevel, "Removing temp dir "+extractDir, os.RemoveAll(extractDir))
		log.Error(templateRef + " exists")
	}

	parent := container.GetConfigItem(extractDir+"/config", "subutai.parent")
	parentOwner := container.GetConfigItem(extractDir+"/config", "subutai.parent.owner")
	parentVersion := container.GetConfigItem(extractDir+"/config", "subutai.parent.version")

	parentRef := strings.Join([]string{parent, parentOwner, parentVersion}, ":")
	if parentRef != templateRef && !container.IsTemplate(parentRef) && !stringInList(parentRef, auxDepList) {
		// Append the template and parent name to dependency list
		auxDepList = append(auxDepList, parentRef, templateRef)
		log.Info("Parent template required: " + parentRef)
		LxcImport(parentRef, token, auxDepList...)
	}

	//!important used by Console
	log.Info("Installing template " + t.Name)

	//delete dataset if already exists
	if fs.DatasetExists(templateRef) {
		container.Destroy(templateRef, true)
	}

	install(templateRef)

	log.Check(log.WarnLevel, "Removing temp dir "+extractDir, os.RemoveAll(extractDir))

	//delete template archive
	if !local {
		log.Check(log.WarnLevel, "Removing file: "+localArchive, os.Remove(localArchive))
	}

	if t.Name == container.Management {
		initManagement(templateRef)
		return
	}

	log.Check(log.ErrorLevel, "Setting lxc config", updateContainerConfig(templateRef))
}

func download(template Template) {

	if isValidUrl(config.CDN.TemplateDownloadUrl) {
		downloadFromGateway(template)
	} else {
		downloadViaLocalIPFSNode(template)
	}

}

func isValidUrl(toTest string) bool {
	_, err := url.ParseRequestURI(toTest)
	if err != nil {
		return false
	} else {
		return true
	}
}

func getTemplateUrl(template Template) string {

	directUrl := strings.Replace(config.CDN.TemplateDownloadUrl, "{ID}", template.Id, 1)

	u, err := url.Parse(directUrl)
	log.Check(log.ErrorLevel, "Parsing template url", err)

	u.Path = path.Join(u.Path, template.Name)
	wrappedUrl := u.String() + wrappedTemplateSuffix
	res, err := http.Head(wrappedUrl)
	log.Check(log.ErrorLevel, "Checking wrapped template existence", err)
	if res.StatusCode == 200 {
		return wrappedUrl
	}

	res, err = http.Head(directUrl)
	log.Check(log.ErrorLevel, "Checking template existence", err)
	if res.StatusCode == 200 {
		return directUrl
	}

	log.Error("Template not found")

	//should not reach here
	return ""
}

func isWrappedTemplateUrl(url string) bool {
	return strings.HasSuffix(url, wrappedTemplateSuffix)
}

func downloadFromGateway(template Template) {
	templateUrl := getTemplateUrl(template)
	attempts := 1
	var err error

	for err = doDownload(template, templateUrl); err != nil && attempts < maxDownloadAttempts; err = doDownload(template, templateUrl) {
		attempts++
	}

	log.Check(log.ErrorLevel, "Download completed", err)
}

func doDownload(template Template, templateUrl string) error {
	templatePath := path.Join(config.Agent.CacheDir, template.Id)

	isWrapped := isWrappedTemplateUrl(templateUrl)
	wrappedSuffix := ""
	if isWrapped {
		wrappedSuffix = "_wrap"
	}

	// create client
	client := grab.NewClient()

	req, err := grab.NewRequest(templatePath+wrappedSuffix, templateUrl)

	if log.Check(log.DebugLevel, fmt.Sprintf("Preparing request %v", req.URL()), err) {
		return err
	}

	//!important used by Console
	log.Info("Downloading " + template.Name)

	// start download
	resp := client.Do(req)

	if resp.HTTPResponse != nil {
		log.Debug("Http status ", resp.HTTPResponse.Status)
	}

	// start UI loop
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()

	bar := pb.New(int(resp.Size)).SetUnits(pb.U_BYTES)
	if resp.Size <= 0 {
		bar.NotPrint = true
	}
	bar.Start()
	defer bar.Finish()
Loop:
	for {
		select {
		case <-t.C:
			bar.Set(int(resp.BytesComplete()))

		case <-resp.Done:
			// download is complete
			bar.Set(int(resp.BytesComplete()))
			break Loop
		}
	}

	bar.Finish()

	// check for errors
	if log.Check(log.DebugLevel, "Checking download status", resp.Err()) {
		return err
	}

	if isWrapped {
		//rename template
		os.RemoveAll(templatePath)
		log.Check(log.ErrorLevel, "Renaming template", os.Rename(templatePath+wrappedSuffix, templatePath))
	}

	//check hash sum
	if !verifyChecksum(template, templatePath) {
		return errors.New("File integrity verification failed")
	}

	return nil
}

func downloadViaLocalIPFSNode(template Template) {
	log.Debug("Checking template availability in CDN network...")

	//check local node
	_, err := exec.ExecuteWithBash("ipfs refs local | grep " + template.Id)

	if err != nil {
		//check network
		err = exec.Exec("ipfs", "--timeout=600s", "dht", "findprovs", "-n1", template.Id)
	}

	if err != nil {
		log.Fatal("Template not found in CDN network")
	}

	//!important used by Console
	log.Info("Downloading " + template.Name)

	templatePath := path.Join(config.Agent.CacheDir, template.Id)

	//download template
	_, err = exec.ExecuteOutput("ipfs", map[string]string{"IPFS_PATH": config.CDN.IpfsPath}, "get", template.Id, "-o", templatePath)
	log.Check(log.FatalLevel, "Checking download status", err)

	//check if download is a directory
	isDir, err := fs.IsDir(templatePath)
	log.Check(log.ErrorLevel, "Checking if file is directory", err)

	if isDir {
		//move template archive outside
		archivePath := path.Join(templatePath, template.Name+wrappedTemplateSuffix)
		tmpPath := path.Join(config.Agent.CacheDir, template.Name+wrappedTemplateSuffix)
		os.RemoveAll(tmpPath)
		log.Check(log.ErrorLevel, "Moving template archive out of wrapping directory", os.Rename(archivePath, tmpPath))
		//remove directory and rename archive
		os.RemoveAll(templatePath)
		log.Check(log.ErrorLevel, "Restoring template archive path", os.Rename(tmpPath, templatePath))

	}

	//verify its md5 sum
	if !verifyChecksum(template, templatePath) {
		log.Fatal("File integrity verification failed")
	}

	//pin template
	exec.Exec("ipfs", "pin", "add", template.Id)
}

func updateContainerConfig(templateName string) error {

	if common.GetMajorVersion() < 3 {
		return container.SetContainerConf(templateName, [][]string{
			{"lxc.rootfs", path.Join(config.Agent.LxcPrefix, templateName, "rootfs")},
			{"lxc.mount.entry", path.Join(config.Agent.LxcPrefix, templateName, "home") + " home none bind,rw 0 0"},
			{"lxc.mount.entry", path.Join(config.Agent.LxcPrefix, templateName, "opt") + " opt none bind,rw 0 0"},
			{"lxc.mount.entry", path.Join(config.Agent.LxcPrefix, templateName, "var") + " var none bind,rw 0 0"},
		})
	} else {
		return container.SetContainerConf(templateName, [][]string{
			// TODO: Check this line
			{"lxc.rootfs.path", "zfs:" + path.Join(config.Agent.LxcPrefix, templateName, "rootfs")},
			{"lxc.mount.entry", path.Join(config.Agent.LxcPrefix, templateName, "home") + " home none bind,rw 0 0"},
			{"lxc.mount.entry", path.Join(config.Agent.LxcPrefix, templateName, "opt") + " opt none bind,rw 0 0"},
			{"lxc.mount.entry", path.Join(config.Agent.LxcPrefix, templateName, "var") + " var none bind,rw 0 0"},
		})
	}

}

// Verify if package is already on dependency list
func stringInList(s string, list []string) bool {
	for _, i := range list {
		if s == i {
			return true
		}
	}
	return false
}

func install(templateName string) error {

	pathToDecompressedTemplate := path.Join(config.Agent.CacheDir, templateName)

	// create parent dataset
	err := fs.CreateDataset(templateName)
	if err != nil {
		return err
	}
	// create partitions
	err = fs.ReceiveStream(templateName+"/rootfs", path.Join(pathToDecompressedTemplate, "deltas", "rootfs.delta"), false)
	if err != nil {
		return err
	}
	err = fs.ReceiveStream(templateName+"/home", path.Join(pathToDecompressedTemplate, "deltas", "home.delta"), false)
	if err != nil {
		return err
	}
	err = fs.ReceiveStream(templateName+"/var", path.Join(pathToDecompressedTemplate, "deltas", "var.delta"), false)
	if err != nil {
		return err
	}
	err = fs.ReceiveStream(templateName+"/opt", path.Join(pathToDecompressedTemplate, "deltas", "opt.delta"), false)
	if err != nil {
		return err
	}

	// set partitions as read-only
	err = fs.SetDatasetReadOnly(templateName + "/rootfs")
	if err != nil {
		return err
	}
	err = fs.SetDatasetReadOnly(templateName + "/home")
	if err != nil {
		return err
	}
	err = fs.SetDatasetReadOnly(templateName + "/var")
	if err != nil {
		return err
	}
	err = fs.SetDatasetReadOnly(templateName + "/opt")
	if err != nil {
		return err
	}

	err = fs.Copy(path.Join(pathToDecompressedTemplate, "config"), path.Join(config.Agent.LxcPrefix, templateName, "config"))
	if err != nil {
		return err
	}

	return nil
}

func initManagement(templateRef string) {
	container.Clone(templateRef, container.Management)

	container.SetContainerUID(container.Management)
	if common.GetMajorVersion() < 3 {
		container.SetContainerConf(container.Management, [][]string{
			{"lxc.network.veth.pair", container.Management},
		})
	} else {
		container.SetContainerConf(container.Management, [][]string{
			{"lxc.net.0.veth.pair", container.Management},
		})

	}
	gpg.GenerateKey(container.Management)
	container.SetDNS(container.Management)
	container.SetManagementNet()
	container.Start(container.Management)

	//TODO use proxy lib
	log.Check(log.WarnLevel, "Setting up proxy for port 8443",
		exec.Exec("subutai", "proxy", "create", "-t", "management-8443", "-p", "tcp", "-e", "8443"))
	log.Check(log.WarnLevel, "Redirecting port 8443 to management container",
		exec.Exec("subutai", "proxy", "srv", "add", "-t", "management-8443", "-s", "10.10.10.1:8443"))
	log.Check(log.WarnLevel, "Setting up proxy for port 8444",
		exec.Exec("subutai", "proxy", "create", "-t", "management-8444", "-p", "tcp", "-e", "8444"))
	log.Check(log.WarnLevel, "Redirecting port 8444 to management container",
		exec.Exec("subutai", "proxy", "srv", "add", "-t", "management-8444", "-s", "10.10.10.1:8444"))
	log.Check(log.WarnLevel, "Setting up proxy for port 8086",
		exec.Exec("subutai", "proxy", "create", "-t", "management-8086", "-p", "tcp", "-e", "8086"))
	log.Check(log.WarnLevel, "Redirecting port 8086 to management container",
		exec.Exec("subutai", "proxy", "srv", "add", "-t", "management-8086", "-s", "10.10.10.1:8086"))

	mgmtCont := &db.Container{}
	mgmtCont.Name = container.Management
	mgmtCont.Ip = container.ManagementIp
	mgmtCont.State = container.Running
	log.Check(log.ErrorLevel, "Writing container data to database", db.SaveContainer(mgmtCont))

	log.Info("********************")
	log.Info("Subutai Management UI will be shortly available at https://" + net.GetIp() + ":8443")
	log.Info("login: admin")
	log.Info("password: secret")
	log.Info("********************")
}
