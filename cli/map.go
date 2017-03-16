package cli

import (
	"io/ioutil"
	"math/rand"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/subutai-io/agent/config"
	"github.com/subutai-io/agent/db"
	"github.com/subutai-io/agent/lib/fs"
	"github.com/subutai-io/agent/lib/gpg"
	ovs "github.com/subutai-io/agent/lib/net"
	"github.com/subutai-io/agent/log"
)

func MapPort(protocol, internal, external, domain, cert string, remove bool) {
	if protocol != "tcp" && protocol != "udp" && protocol != "http" && protocol != "https" {
		log.Error("Unsupported protocol \"" + protocol + "\"")
	}

	if remove {
		mapRemove(protocol, internal, external)
		return
	}
	//validate args
	if (protocol == "http" || protocol == "https") && len(domain) == 0 {
		log.Error("\"-d domain\" is mandatory for http protocol")
	}

	if protocol == "https" && (len(cert) == 0 || !gpg.ValidatePem(cert)) {
		log.Error("\"-c certificate\" is missing or invalid pem file")
	}

	if !validSocket(internal) {
		log.Error("Parameter \"internal\" should be in ip:port format")
	}

	// check external port and create nginx config
	if portIsNew(protocol, internal, domain, &external) {
		newConfig(protocol, external, domain, cert)
	}

	// add containers to backend
	addLine(config.Agent.DataPrefix+"nginx-includes/"+protocol+"/"+external+".conf",
		"#Add new host here", "server "+internal+";", false)

	// save information to database
	saveMapToDB(protocol, internal, external, domain)

	// reload nginx
	restart()

	log.Info(ovs.GetIp() + ":" + external)
}

func mapRemove(protocol, internal, external string) {
	bolt, err := db.New()
	log.Check(log.ErrorLevel, "Openning portmap database", err)
	defer bolt.Close()
	if !bolt.PortInMap(protocol, external, internal) {
		return
	}
	l := bolt.PortMapDelete(protocol, internal, external)

	if l > 0 {
		if strings.Contains(internal, ":") {
			internal = internal + ";"
		} else {
			internal = internal + ":"
		}
		addLine(config.Agent.DataPrefix+"nginx-includes/"+protocol+"/"+external+".conf",
			"server "+internal, " ", true)
	} else {
		os.Remove(config.Agent.DataPrefix + "nginx-includes/" + protocol + "/" + external + ".conf")
		os.Remove(config.Agent.DataPrefix + "web/ssl/https-" + external + ".key")
		os.Remove(config.Agent.DataPrefix + "web/ssl/https-" + external + ".crt")
	}
	restart()
}

func isFree(protocol, port string) (res bool) {
	switch protocol {
	case "tcp", "http", "https":
		if ln, err := net.Listen("tcp", ovs.GetIp()+":"+port); err == nil {
			ln.Close()
			res = true
		}
	case "udp":
		if addr, err := net.ResolveUDPAddr("udp", ovs.GetIp()+":"+port); err == nil {
			if ln, err := net.ListenUDP("udp", addr); err == nil {
				ln.Close()
				res = true
			}
		}
	}
	return
}

func random(min, max int) int {
	rand.Seed(time.Now().Unix())
	return rand.Intn(max-min) + min
}

func validSocket(socket string) bool {
	if addr := strings.Split(socket, ":"); len(addr) == 2 {
		if _, err := net.ResolveIPAddr("ip4", addr[0]); err == nil {
			if port, err := strconv.Atoi(addr[1]); err == nil && port < 65536 {
				return true
			}
		}
	}
	return false
}

func portIsNew(protocol, internal, domain string, external *string) (new bool) {
	if len(*external) != 0 {
		if port, err := strconv.Atoi(*external); err != nil || port < 1000 || port > 65536 {
			log.Error("Parameter \"external\" should be integer in range of 1000-65536")
		}
		if isFree(protocol, *external) {
			new = true
		} else {
			bolt, err := db.New()
			log.Check(log.ErrorLevel, "Openning portmap database", err)
			if bolt.PortInMap(protocol, *external, internal) {
				log.Error("Map is already exists")
			} else if !bolt.PortInMap(protocol, *external, "") {
				log.Error("Port is busy")
			}
			log.Check(log.WarnLevel, "Closing database", bolt.Close())
		}
	} else {
		for *external = strconv.Itoa(random(1000, 65536)); !isFree(protocol, *external); *external = strconv.Itoa(random(1000, 65536)) {
			continue
		}
		new = true
	}
	return
}

func newConfig(protocol, port, domain, cert string) {
	log.Check(log.WarnLevel, "Creating nginx include folder",
		os.MkdirAll(config.Agent.DataPrefix+"nginx-includes/"+protocol, 0755))

	switch protocol {
	case "https":
		log.Check(log.ErrorLevel, "Creating certificate dirs", os.MkdirAll(config.Agent.DataPrefix+"/web/ssl/", 0755))
		fs.Copy(config.Agent.AppPrefix+"etc/nginx/tmpl/vhost-ssl.example",
			config.Agent.DataPrefix+"nginx-includes/"+protocol+"/"+port+".conf")
		addLine(config.Agent.DataPrefix+"nginx-includes/"+protocol+"/"+port+".conf",
			"listen      80;", "listen "+port+";", true)
		addLine(config.Agent.DataPrefix+"nginx-includes/"+protocol+"/"+port+".conf",
			"listen	443;", "listen "+port+";", true)
		addLine(config.Agent.DataPrefix+"nginx-includes/"+protocol+"/"+port+".conf",
			"server_name DOMAIN;", "server_name "+domain+";", true)
		addLine(config.Agent.DataPrefix+"nginx-includes/"+protocol+"/"+port+".conf",
			"proxy_pass http://DOMAIN-upstream/;", "proxy_pass http://https-"+port+";", true)
		addLine(config.Agent.DataPrefix+"nginx-includes/"+protocol+"/"+port+".conf",
			"upstream DOMAIN-upstream {", "upstream https-"+port+" {", true)

		crt, key := gpg.ParsePem(cert)
		log.Check(log.WarnLevel, "Writing certificate body", ioutil.WriteFile(config.Agent.DataPrefix+"web/ssl/https-"+port+".crt", crt, 0644))
		log.Check(log.WarnLevel, "Writing key body", ioutil.WriteFile(config.Agent.DataPrefix+"web/ssl/https-"+port+".key", key, 0644))

		addLine(config.Agent.DataPrefix+"nginx-includes/"+protocol+"/"+port+".conf",
			"ssl_certificate /var/snap/subutai/current/web/ssl/UNIXDATE.crt;",
			"ssl_certificate "+config.Agent.DataPrefix+"web/ssl/https-"+port+".crt;", true)
		addLine(config.Agent.DataPrefix+"nginx-includes/"+protocol+"/"+port+".conf",
			"ssl_certificate_key /var/snap/subutai/current/web/ssl/UNIXDATE.key;",
			"ssl_certificate_key "+config.Agent.DataPrefix+"web/ssl/https-"+port+".key;", true)
	case "http":
		fs.Copy(config.Agent.AppPrefix+"etc/nginx/tmpl/vhost.example",
			config.Agent.DataPrefix+"nginx-includes/"+protocol+"/"+port+".conf")
		addLine(config.Agent.DataPrefix+"nginx-includes/"+protocol+"/"+port+".conf",
			"listen 	80;", "listen "+port+";", true)
		addLine(config.Agent.DataPrefix+"nginx-includes/"+protocol+"/"+port+".conf",
			"server_name DOMAIN;", "server_name "+domain+";", true)
		addLine(config.Agent.DataPrefix+"nginx-includes/"+protocol+"/"+port+".conf",
			"proxy_pass http://DOMAIN-upstream/;", "proxy_pass http://http-"+port+";", true)
		addLine(config.Agent.DataPrefix+"nginx-includes/"+protocol+"/"+port+".conf",
			"upstream DOMAIN-upstream {", "upstream http-"+port+" {", true)
	case "tcp":
		fs.Copy(config.Agent.AppPrefix+"etc/nginx/tmpl/stream.example",
			config.Agent.DataPrefix+"nginx-includes/"+protocol+"/"+port+".conf")
		addLine(config.Agent.DataPrefix+"nginx-includes/"+protocol+"/"+port+".conf",
			"listen PORT;", "listen "+port+";", true)
	case "udp":
		fs.Copy(config.Agent.AppPrefix+"etc/nginx/tmpl/stream.example",
			config.Agent.DataPrefix+"nginx-includes/"+protocol+"/"+port+".conf")
		addLine(config.Agent.DataPrefix+"nginx-includes/"+protocol+"/"+port+".conf",
			"listen PORT;", "listen "+port+" udp;", true)
	}
	addLine(config.Agent.DataPrefix+"nginx-includes/"+protocol+"/"+port+".conf",
		"server localhost:81;", " ", true)
	addLine(config.Agent.DataPrefix+"nginx-includes/"+protocol+"/"+port+".conf",
		"upstream PROTO-PORT {", "upstream "+protocol+"-"+port+" {", true)
	addLine(config.Agent.DataPrefix+"nginx-includes/"+protocol+"/"+port+".conf",
		"proxy_pass PROTO-PORT;", "proxy_pass "+protocol+"-"+port+";", true)
}

func saveMapToDB(protocol, internal, external, domain string) {
	bolt, err := db.New()
	ops := make(map[string]string)
	log.Check(log.ErrorLevel, "Openning portmap database", err)
	c := bolt.ContainerByKey("ip", strings.Split(internal, ":")[0])
	if len(c) > 0 {
		ops["container"] = c[0]
	}
	if len(domain) > 0 {
		ops["domain"] = domain
	}
	log.Check(log.WarnLevel, "Saving port map to database", bolt.PortMapSet(protocol, internal, external, ops))
	log.Check(log.WarnLevel, "Closing database", bolt.Close())
}
