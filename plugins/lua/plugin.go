package alpslua

import "alpi/websrv"

func init() {
	websrv.RegisterPluginLoader(loadAllLuaPlugins)
}
