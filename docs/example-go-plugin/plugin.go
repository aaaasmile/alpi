// Package exampleplugin is an example Go plugin for alps.
//
// To enable it, import this package from cmd/alps/main.go.
package exampleplugin

import (
	"fmt"
	"net/http"

	alpsbase "alpi/plugins/base"
	"alpi/websrv"
)

func init() {
	p := websrv.GoPlugin{Name: "example"}

	// Setup a function called when the mailbox view is rendered
	p.Inject("mailbox.html", func(ctx *websrv.Context, kdata websrv.RenderData) error {
		data := kdata.(*alpsbase.MailboxRenderData)
		fmt.Println("The mailbox view for " + data.Mailbox.Name + " is being rendered")
		// Set extra data that can be accessed from the mailbox.html template
		data.Extra["Example"] = "Hi from Go"
		return nil
	})

	// Wire up a new route
	p.GET("/example", func(ctx *websrv.Context) error {
		return ctx.String(http.StatusOK, "This is an example page.")
	})

	// Register a helper function that can be called from templates
	p.TemplateFuncs(map[string]interface{}{
		"example_and": func(a, b string) string {
			return a + " and " + b
		},
	})

	websrv.RegisterPluginLoader(p.Loader())
}
