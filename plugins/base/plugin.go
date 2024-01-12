package alpsbase

import "alpi/websrv"

func init() {
	p := websrv.GoPlugin{Name: "base"}

	p.TemplateFuncs(templateFuncs)
	registerRoutes(&p)

	websrv.RegisterPluginLoader(p.Loader())
}
