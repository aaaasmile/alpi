package alpsviewcalendar

import "alpi/websrv"

func init() {
	p := websrv.GoPlugin{Name: "viewcalendar"}
	websrv.RegisterPluginLoader(p.Loader())
}
