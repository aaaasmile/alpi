package alpsviewhtml

import (
	"bytes"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	alpsbase "alpi/plugins/base"

	"github.com/aymerick/douceur/css"
	cssparser "github.com/chris-ramon/douceur/parser"
	"github.com/microcosm-cc/bluemonday"
	"golang.org/x/net/html"
)

// TODO: this doesn't accomodate for quoting
var (
	cssURLRegexp  = regexp.MustCompile(`url\([^)]*\)`)
	cssExprRegexp = regexp.MustCompile(`expression\([^)]*\)`)
)

var allowedStyles = map[string]bool{
	"direction":       true,
	"font":            true,
	"font-family":     true,
	"font-style":      true,
	"font-variant":    true,
	"font-size":       true,
	"font-weight":     true,
	"letter-spacing":  true,
	"line-height":     true,
	"text-align":      true,
	"text-decoration": true,
	"text-indent":     true,
	"text-overflow":   true,
	"text-shadow":     true,
	"text-transform":  true,
	"white-space":     true,
	"word-spacing":    true,
	"word-wrap":       true,
	"vertical-align":  true,

	"color":             true,
	"background":        true,
	"background-color":  true,
	"background-image":  true,
	"background-repeat": true,

	"border":        true,
	"border-color":  true,
	"border-radius": true,
	"height":        true,
	"margin":        true,
	"padding":       true,
	"width":         true,
	"max-width":     true,
	"min-width":     true,

	"clear": true,
	"float": true,

	"border-collapse": true,
	"border-spacing":  true,
	"caption-side":    true,
	"empty-cells":     true,
	"table-layout":    true,

	"list-style-type":     true,
	"list-style-position": true,
}

type sanitizer struct {
	msg                  *alpsbase.IMAPMessage
	allowRemoteResources bool
	hasRemoteResources   bool
}

func (san *sanitizer) sanitizeImageURL(src string) string {
	u, err := url.Parse(src)
	if err != nil {
		return "about:blank"
	}

	switch strings.ToLower(u.Scheme) {
	// TODO: mid support?
	case "cid":
		if san.msg == nil {
			return "about:blank"
		}

		part := san.msg.PartByID(u.Opaque)
		if part == nil || !strings.HasPrefix(part.MIMEType, "image/") {
			return "about:blank"
		}

		return part.URL(true).String()
	case "https":
		san.hasRemoteResources = true

		if !proxyEnabled || !san.allowRemoteResources {
			return "about:blank"
		}

		proxyURL := url.URL{Path: "/proxy"}
		proxyQuery := make(url.Values)
		proxyQuery.Set("src", u.String())
		proxyURL.RawQuery = proxyQuery.Encode()
		return proxyURL.String()
	default:
		return "about:blank"
	}
}

func (san *sanitizer) sanitizeCSSDecls(decls []*css.Declaration) []*css.Declaration {
	sanitized := make([]*css.Declaration, 0, len(decls))
	for _, decl := range decls {
		if !allowedStyles[decl.Property] {
			continue
		}
		if cssExprRegexp.FindStringIndex(decl.Value) != nil {
			continue
		}

		// TODO: more robust CSS declaration parsing
		decl.Value = cssURLRegexp.ReplaceAllString(decl.Value, "url(about:blank)")

		sanitized = append(sanitized, decl)
	}
	return sanitized
}

func (san *sanitizer) sanitizeCSSRule(rule *css.Rule) {
	// Disallow @import
	if rule.Kind == css.AtRule && strings.EqualFold(rule.Name, "@import") {
		rule.Prelude = "url(about:blank)"
	}

	rule.Declarations = san.sanitizeCSSDecls(rule.Declarations)

	for _, child := range rule.Rules {
		san.sanitizeCSSRule(child)
	}
}

func (san *sanitizer) sanitizeNode(n *html.Node) {
	if n.Type == html.ElementNode {
		if strings.EqualFold(n.Data, "img") {
			for i := range n.Attr {
				attr := &n.Attr[i]
				if strings.EqualFold(attr.Key, "src") {
					attr.Val = san.sanitizeImageURL(attr.Val)
				}
			}
		} else if strings.EqualFold(n.Data, "style") {
			var s string
			c := n.FirstChild
			for c != nil {
				if c.Type == html.TextNode {
					s += c.Data
				}

				next := c.NextSibling
				n.RemoveChild(c)
				c = next
			}

			stylesheet, err := cssparser.Parse(s)
			if err != nil {
				s = ""
			} else {
				for _, rule := range stylesheet.Rules {
					san.sanitizeCSSRule(rule)
				}

				s = stylesheet.String()
			}

			n.AppendChild(&html.Node{
				Type: html.TextNode,
				Data: s,
			})
		}

		for i := range n.Attr {
			// Don't use `i, attr := range n.Attr` since `attr` would be a copy
			attr := &n.Attr[i]

			if strings.EqualFold(attr.Key, "style") {
				decls, err := cssparser.ParseDeclarations(attr.Val)
				if err != nil {
					attr.Val = ""
					continue
				}

				decls = san.sanitizeCSSDecls(decls)

				attr.Val = ""
				for _, d := range decls {
					attr.Val += d.String()
				}
			}
		}
	}

	for c := n.FirstChild; c != nil; c = c.NextSibling {
		san.sanitizeNode(c)
	}
}

func (san *sanitizer) sanitizeHTML(b []byte) ([]byte, error) {
	doc, err := html.Parse(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("failed to parse HTML: %v", err)
	}

	san.sanitizeNode(doc)

	var buf bytes.Buffer
	if err := html.Render(&buf, doc); err != nil {
		return nil, fmt.Errorf("failed to render HTML: %v", err)
	}
	b = buf.Bytes()

	// bluemonday must always be run last
	p := bluemonday.UGCPolicy()

	// TODO: use bluemonday's AllowStyles once it's released and
	// supports <style>
	p.AllowElements("style")
	p.AllowAttrs("style").Globally()

	p.AddTargetBlankToFullyQualifiedLinks(true)
	p.RequireNoFollowOnLinks(true)

	return p.SanitizeBytes(b), nil
}
