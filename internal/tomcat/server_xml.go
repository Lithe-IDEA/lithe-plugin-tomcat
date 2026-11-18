package tomcat

import (
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type ServerInfo struct {
	XMLValid        bool   `json:"xmlValid"`
	AppBase         string `json:"appBase"`
	AutoDeploy      bool   `json:"autoDeploy"`
	DeployOnStartup bool   `json:"deployOnStartup"`
	HTTPPorts       []int  `json:"httpPorts"`
}

type serverXML struct {
	Services []struct {
		Connectors []struct {
			Port     string `xml:"port,attr"`
			Protocol string `xml:"protocol,attr"`
		} `xml:"Connector"`
		Engine struct {
			Hosts []struct {
				Name            string `xml:"name,attr"`
				AppBase         string `xml:"appBase,attr"`
				AutoDeploy      string `xml:"autoDeploy,attr"`
				DeployOnStartup string `xml:"deployOnStartup,attr"`
			} `xml:"Host"`
		} `xml:"Engine"`
	} `xml:"Service"`
}

// inspectServerXML does not expand entities or external schemas. Unknown or
// malformed server.xml disables online deployment rather than assuming it is
// enabled. Ordinary offline Run/Deploy still works with a standard appBase.
func inspectServerXML(base string) ServerInfo {
	result := ServerInfo{AppBase: "webapps"}
	p := filepath.Join(base, "conf", "server.xml")
	raw, err := os.ReadFile(p)
	if err != nil || len(raw) > 2<<20 {
		return result
	}
	var v serverXML
	if xml.Unmarshal(raw, &v) != nil {
		return result
	}
	result.XMLValid = true
	for _, svc := range v.Services {
		for _, c := range svc.Connectors {
			if strings.Contains(strings.ToUpper(c.Protocol), "AJP") {
				continue
			}
			port, e := strconv.Atoi(c.Port)
			if e == nil && port > 0 && port <= 65535 && len(result.HTTPPorts) < 16 {
				result.HTTPPorts = append(result.HTTPPorts, port)
			}
		}
	}
	for _, svc := range v.Services {
		for _, h := range svc.Engine.Hosts {
			if h.Name != "" && h.Name != "localhost" {
				continue
			}
			if h.AppBase != "" {
				result.AppBase = h.AppBase
			}
			result.AutoDeploy = h.AutoDeploy != "false"
			result.DeployOnStartup = h.DeployOnStartup != "false"
			return result
		}
	}
	// Cannot identify a Host; online deploy requires explicit evidence.
	result.XMLValid = false
	return result
}

func checkedAppBase(base string, i ServerInfo) (string, error) {
	if filepath.IsAbs(i.AppBase) || i.AppBase == "" || strings.HasPrefix(i.AppBase, "..") || strings.ContainsAny(i.AppBase, ":\\") {
		return "", errors.New("custom absolute or parent Tomcat Host appBase is unsupported; use a Catalina base with a local appBase")
	}
	full := filepath.Join(base, filepath.Clean(i.AppBase))
	if !equalOrWithin(base, full) {
		return "", errors.New("Tomcat Host appBase escapes Catalina base")
	}
	return full, nil
}
