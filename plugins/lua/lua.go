package alpslua

import (
	"alpi/websrv"
	"fmt"
	"html/template"
	"path/filepath"

	"github.com/labstack/echo/v4"
	lua "github.com/yuin/gopher-lua"
	luar "layeh.com/gopher-luar"
)

type luaRoute struct {
	method string
	path   string
	f      *lua.LFunction
}

type luaPlugin struct {
	filename        string
	state           *lua.LState
	renderCallbacks map[string]*lua.LFunction
	filters         template.FuncMap
	routes          []luaRoute
}

func (p *luaPlugin) Name() string {
	return p.filename
}

func (p *luaPlugin) onRender(l *lua.LState) int {
	name := l.CheckString(1)
	f := l.CheckFunction(2)
	p.renderCallbacks[name] = f
	return 0
}

func (p *luaPlugin) setFilter(l *lua.LState) int {
	name := l.CheckString(1)
	f := l.CheckFunction(2)
	p.filters[name] = func(args ...interface{}) string {
		luaArgs := make([]lua.LValue, len(args))
		for i, v := range args {
			luaArgs[i] = luar.New(l, v)
		}

		err := l.CallByParam(lua.P{
			Fn:      f,
			NRet:    1,
			Protect: true,
		}, luaArgs...)
		if err != nil {
			panic(err) // TODO: better error handling?
		}

		ret := l.CheckString(-1)
		l.Pop(1)
		return ret
	}
	return 0
}

func (p *luaPlugin) setRoute(l *lua.LState) int {
	method := l.CheckString(1)
	path := l.CheckString(2)
	f := l.CheckFunction(3)
	p.routes = append(p.routes, luaRoute{method, path, f})
	return 0
}

func (p *luaPlugin) inject(name string, data websrv.RenderData) error {
	f, ok := p.renderCallbacks[name]
	if !ok {
		return nil
	}

	err := p.state.CallByParam(lua.P{
		Fn:      f,
		NRet:    0,
		Protect: true,
	}, luar.New(p.state, data))
	if err != nil {
		return err
	}

	return nil
}

func (p *luaPlugin) Inject(ctx *websrv.Context, name string, data websrv.RenderData) error {
	if err := p.inject("*", data); err != nil {
		return err
	}
	return p.inject(name, data)
}

func (p *luaPlugin) LoadTemplate(t *template.Template) error {
	t.Funcs(p.filters)

	paths, err := filepath.Glob(filepath.Dir(p.filename) + "/public/*.html")
	if err != nil {
		return err
	}
	if len(paths) > 0 {
		if _, err := t.ParseFiles(paths...); err != nil {
			return err
		}
	}

	return nil
}

func (p *luaPlugin) SetRoutes(group *echo.Group) {
	for _, r := range p.routes {
		group.Match([]string{r.method}, r.path, func(ctx echo.Context) error {
			err := p.state.CallByParam(lua.P{
				Fn:      r.f,
				NRet:    0,
				Protect: true,
			}, luar.New(p.state, ctx))
			if err != nil {
				return fmt.Errorf("Lua plugin error: %v", err)
			}

			return nil
		})
	}

	_, name := filepath.Split(filepath.Dir(p.filename))
	group.Static("/plugins/"+name+"/assets", filepath.Dir(p.filename)+"/public/assets")
}

func (p *luaPlugin) Close() error {
	p.state.Close()
	return nil
}

func loadLuaPlugin(filename string) (*luaPlugin, error) {
	l := lua.NewState()
	p := &luaPlugin{
		filename:        filename,
		state:           l,
		renderCallbacks: make(map[string]*lua.LFunction),
		filters:         make(template.FuncMap),
	}

	mt := l.NewTypeMetatable("alps")
	l.SetGlobal("alps", mt)
	l.SetField(mt, "on_render", l.NewFunction(p.onRender))
	l.SetField(mt, "set_filter", l.NewFunction(p.setFilter))
	l.SetField(mt, "set_route", l.NewFunction(p.setRoute))

	if err := l.DoFile(filename); err != nil {
		l.Close()
		return nil, err
	}

	return p, nil
}

func loadAllLuaPlugins(s *websrv.Server) ([]websrv.Plugin, error) {
	log := s.Logger()

	filenames, err := filepath.Glob(websrv.PluginDir + "/*/main.lua")
	if err != nil {
		return nil, fmt.Errorf("filepath.Glob failed: %v", err)
	}

	plugins := make([]websrv.Plugin, 0, len(filenames))
	for _, filename := range filenames {
		log.Printf("Loading Lua plugin %q", filename)

		p, err := loadLuaPlugin(filename)
		if err != nil {
			for _, p := range plugins {
				p.Close()
			}
			return nil, fmt.Errorf("failed to load Lua plugin %q: %v", filename, err)
		}
		plugins = append(plugins, p)
	}

	return plugins, nil
}
