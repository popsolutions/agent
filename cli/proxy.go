package cli

import (
	"io/ioutil"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/subutai-io/agent/config"
	"github.com/subutai-io/agent/lib/container"
	"github.com/subutai-io/agent/lib/fs"
	"github.com/subutai-io/agent/lib/gpg"
	"github.com/subutai-io/agent/log"
	"path"
)

var (
	conftmpl   = "/etc/subutai/nginx/tmpl"
	confinc    = path.Join(config.Agent.DataPrefix, "nginx/nginx-includes/http")
	webSslPath = path.Join(config.Agent.DataPrefix, "/web/ssl")
)

// The reverse proxy component in Subutai provides and easy way to assign domain name and forward HTTP(S) traffic to certain environment.
// The proxy binding is used to manage Subutai reverse proxies.
// Each proxy subcommand works with config patterns: adding, removing or checking certain lines, and reloading the proxy daemon if needed, etc.
// The reverse proxy functionality supports three common load balancing strategies - round-robin, load based and "sticky" sessions.
// It can also accept SSL certificates in .pem file format and install it for a domain.

func AddProxyHost(vlan, host string) {
	if vlan == "" {
		log.Error("Please specify VLAN")
	} else if host == "" {
		log.Error("Please specify host (container ip[:port])")
	}

	if hostExists(vlan, host) {
		log.Error("Host is already in domain")
	}
	addHost(vlan, host)
	restart()
}

func AddProxyDomain(vlan, domain, policy, cert string) {
	if vlan == "" {
		log.Error("Please specify VLAN")
	} else if domain == "" {
		log.Error("Please specify domain")
	}

	if vlanExists(vlan) {
		log.Error("Domain already exists")
	}

	if crt := strings.Split(cert, ":"); len(crt) > 1 && container.LxcInstanceExists(crt[0]) {
		if !strings.HasPrefix(crt[1], "/opt/") && !strings.HasPrefix(crt[1], "/var/") && !strings.HasPrefix(crt[1], "/home/") {
			crt[0] += "/rootfs"
		}
		cert = path.Join(config.Agent.LxcPrefix, crt[0], strings.Join(crt[1:], ":"))
	}

	addDomain(vlan, domain, cert)

	switch policy {
	case "rr":
		setPolicy(vlan, "")
	case "lb":
		setPolicy(vlan, "least_conn;")
	case "hash":
		setPolicy(vlan, "ip_hash;")
	}

	restart()
}

func DelProxyDomain(vlan string) {
	if vlan == "" {
		log.Error("Please specify VLAN")
	}

	if vlanExists(vlan) {
		delDomain(vlan)
		restart()
	}
}

func DelProxyHost(vlan, host string) {
	if vlan == "" {
		log.Error("Please specify VLAN")
	}

	if host == "" {
		log.Error("Please specify host (container ip[:port])")
	}

	if vlanExists(vlan) {
		delHost(vlan, host)
		restart()
	}
}

func GetProxyDomain(vlan string) string {
	if vlan == "" {
		log.Error("Please specify VLAN")
	}

	return getDomain(vlan)
}

func IsHostInDomain(vlan, host string) bool {
	if vlan == "" {
		log.Error("Please specify VLAN")
	}

	if host == "" {
		log.Error("Please specify host (container ip[:port])")
	}

	return hostExists(vlan, host)
}

// restart reloads nginx process
func restart() {
	out, err := exec.Command("service", "subutai-nginx", "reload").CombinedOutput()
	log.Check(log.FatalLevel, "Reloading nginx "+string(out), err)
}

// addDomain creates new domain config from pattern and adjusts it
func addDomain(vlan, domain, cert string) {
	if _, err := os.Stat(confinc); os.IsNotExist(err) {
		err := os.MkdirAll(confinc, 0755)
		if err != nil {
			log.Info("Cannot create nginx-include directory " + confinc)
		}
	}
	vlanConf := path.Join(confinc, vlan+".conf")
	if cert != "" && gpg.ValidatePem(cert) {
		currentDT := strconv.Itoa(int(time.Now().Unix()))

		if _, err := os.Stat(webSslPath); os.IsNotExist(err) {
			err := os.MkdirAll(webSslPath, 0755)
			if err != nil {
				log.Info("Cannot create ssl directory " + webSslPath)
				os.Exit(1)
			}
		}

		fs.Copy(path.Join(conftmpl, "vhost-ssl.example"), vlanConf)
		crt, key := gpg.ParsePem(cert)
		err := ioutil.WriteFile(path.Join(webSslPath, currentDT+".crt"), crt, 0644)
		if err != nil {
			log.Info("Cannot create crt file " + path.Join(webSslPath, currentDT+".crt"))
			os.Exit(1)
		}
		err = ioutil.WriteFile(path.Join(webSslPath, currentDT+".key"), key, 0644)
		if err != nil {
			log.Info("Cannot create key file " + path.Join(webSslPath, currentDT+".key"))
			os.Exit(1)
		}
		addLine(vlanConf, "ssl_certificate "+path.Join(webSslPath, "UNIXDATE.crt;"),
			"	ssl_certificate "+path.Join(webSslPath, currentDT+".crt;"), true)
		addLine(vlanConf, "ssl_certificate_key "+path.Join(webSslPath, "UNIXDATE.key;"),
			"	ssl_certificate_key "+path.Join(webSslPath, currentDT+".key;"), true)
	} else {
		fs.Copy(path.Join(conftmpl, "vhost.example"), vlanConf)
	}
	addLine(vlanConf, "upstream DOMAIN-upstream {", "upstream "+domain+"-upstream {", true)
	addLine(vlanConf, "server_name DOMAIN;", "	server_name "+domain+";", true)
	addLine(vlanConf, "proxy_pass http://DOMAIN-upstream/;", "	proxy_pass http://"+domain+"-upstream/;", true)
}

// addHost adds configuration lines to domain configuration
func addHost(vlan, node string) {
	vlanConf := path.Join(confinc, vlan+".conf")

	delLine(vlanConf, "server localhost:81;")
	addLine(vlanConf, "#Add new host here", "	server "+node+"; #$node", false)
}

// delDomain removes domain configuration file and all related stuff
func delDomain(vlan string) {
	vlanConf := path.Join(confinc, vlan+".conf")

	// get and remove cert files
	f, err := ioutil.ReadFile(vlanConf)
	if err != nil {
		log.Fatal("Cannot read nginx virtualhost file:" + vlanConf)
	}
	lines := strings.Split(string(f), "\n")
	for _, v := range lines {
		if strings.Contains(v, "ssl_certificate") || strings.Contains(v, "ssl_certificate_key") {
			line := strings.Fields(v)
			if len(line) > 1 {
				os.Remove(strings.Trim(line[1], ";"))
			}
		}
	}

	os.Remove(vlanConf)
}

// delHost removes node configuration entries from domain config
func delHost(vlan, node string) {
	vlanConf := path.Join(confinc, vlan+".conf")

	delLine(vlanConf, "server "+node+"; #$node")
	delLine(vlanConf, "server "+node+": #$node")
	if hostCount(vlan) == 0 {
		addLine(vlanConf, "#Add new host here", "   server localhost:81;", false)
	}
}

// getDomain returns domain name assigned to specified vlan
func getDomain(vlan string) string {
	f, err := ioutil.ReadFile(path.Join(confinc, vlan+".conf"))
	if err != nil {
		return ""
	}
	lines := strings.Split(string(f), "\n")
	for _, v := range lines {
		if strings.Contains(v, "server_name") {
			line := strings.Fields(v)
			if len(line) > 1 {
				return strings.Trim(line[1], ";")
			}
		}
	}
	return ""
}

// vlanExists is true is domain was configured on specified vlan and false if not
func vlanExists(vlan string) bool {
	if _, err := os.Stat(path.Join(confinc, vlan+".conf")); err == nil {
		return true
	}
	return false
}

// hostExists is true if specified node belongs to vlan, otherwise it is false
func hostExists(vlan, host string) bool {
	return addLine(path.Join(confinc, vlan+".conf"), "server "+host+";", "", false)
}

// hostCount returns the number of nodes assigned to domain on specified vlan
func hostCount(vlan string) int {
	vlanConf := path.Join(confinc, vlan+".conf")
	f, err := ioutil.ReadFile(vlanConf)
	if !log.Check(log.DebugLevel, "Cannot read file "+vlanConf, err) {
		return strings.Count(string(f), "#$node")
	}
	return 0
}

// setPolicy configures load balance policy for domain on specified vlan
func setPolicy(vlan, policy string) {
	vlanConf := path.Join(confinc, vlan+".conf")
	delLine(vlanConf, "ip_hash;")
	delLine(vlanConf, "least_time header;")
	addLine(vlanConf, "#Add new host here", "	"+policy, false)
}

// addLine adds, removes, replaces and checks if line exists in specified file
func addLine(path, after, line string, replace bool) bool {
	f, err := ioutil.ReadFile(path)
	if !log.Check(log.DebugLevel, "Cannot read file "+path, err) {
		lines := strings.Split(string(f), "\n")
		for k, v := range lines {
			if strings.Contains(v, after) {
				if line != "" {
					if replace {
						log.Debug("Replacing " + lines[k] + " with " + line)
						lines[k] = line
					} else {
						log.Debug("Adding " + line + " after " + lines[k])
						lines[k] = after + "\n" + line
					}
				} else {
					return true
				}
			}
		}
		str := strings.Join(lines, "\n")
		log.Check(log.FatalLevel, "Writing new proxy config",
			ioutil.WriteFile(path, []byte(str), 0744))
	}
	return false
}

// delLine removes specified line from file
func delLine(path, line string) {
	var lines2 []string
	f, err := ioutil.ReadFile(path)
	if !log.Check(log.DebugLevel, "Reading config "+path, err) {

		lines := strings.Split(string(f), "\n")
		for _, v := range lines {
			if !strings.Contains(v, line) {
				lines2 = append(lines2, v)
			}
		}
		str := strings.Join(lines2, "\n")
		log.Check(log.FatalLevel, "Writing new proxy config",
			ioutil.WriteFile(path, []byte(str), 0744))
	}
}
