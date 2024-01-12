package alpsviewtext

import "alpi/websrv"

func init() {
	p := websrv.GoPlugin{Name: "viewtext"}
	websrv.RegisterPluginLoader(p.Loader())
}
