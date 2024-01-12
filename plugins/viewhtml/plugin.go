package alpsviewhtml

import (
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	alpsbase "alpi/plugins/base"
	"alpi/websrv"

	"github.com/labstack/echo/v4"
)

var (
	proxyEnabled = true
	proxyMaxSize = 5 * 1024 * 1024 // 5 MiB
)

func init() {
	p := websrv.GoPlugin{Name: "viewhtml"}

	p.Inject("message.html", func(ctx *websrv.Context, _data websrv.RenderData) error {
		data := _data.(*alpsbase.MessageRenderData)
		data.Extra["RemoteResourcesAllowed"] = ctx.QueryParam("allow-remote-resources") == "1"
		hasRemoteResources := false
		if v := ctx.Get("viewhtml.hasRemoteResources"); v != nil {
			hasRemoteResources = v.(bool)
		}
		data.Extra["HasRemoteResources"] = hasRemoteResources
		return nil
	})

	p.GET("/proxy", func(ctx *websrv.Context) error {
		if !proxyEnabled {
			return echo.NewHTTPError(http.StatusForbidden, "proxy disabled")
		}

		u, err := url.Parse(ctx.QueryParam("src"))
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid URL")
		}

		if u.Scheme != "https" {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid scheme")
		}

		resp, err := http.Get(u.String())
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
		if err != nil || !strings.HasPrefix(mediaType, "image/") {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid resource type")
		}

		size, err := strconv.Atoi(resp.Header.Get("Content-Length"))
		if err == nil {
			if size > proxyMaxSize {
				return echo.NewHTTPError(http.StatusBadRequest, "invalid resource length")
			}
			ctx.Response().Header().Set("Content-Length", strconv.Itoa(size))
		}

		lr := io.LimitedReader{R: resp.Body, N: int64(proxyMaxSize)}
		return ctx.Stream(http.StatusOK, mediaType, &lr)
	})

	websrv.RegisterPluginLoader(p.Loader())
}
