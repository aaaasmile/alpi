package alpsbase

import (
	"alpi/websrv"
	"bytes"
	"fmt"
	"html/template"
	"io"
	"mime"
	"net/http"
	"net/textproto"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	imapmove "github.com/emersion/go-imap-move"
	imapclient "github.com/emersion/go-imap/client"
	"github.com/emersion/go-message"
	"github.com/emersion/go-message/mail"
	"github.com/emersion/go-smtp"
	"github.com/labstack/echo/v4"
	"github.com/tkuchiki/go-timezone"
	"jaytaylor.com/html2text"
)

func registerRoutes(p *websrv.GoPlugin) {
	p.GET("/", func(ctx *websrv.Context) error {
		return ctx.Redirect(http.StatusFound, "/mailbox/INBOX")
	})

	p.GET("/mailbox/:mbox", handleGetMailbox)
	p.POST("/mailbox/:mbox", handleGetMailbox)

	p.GET("/new-mailbox", handleNewMailbox)
	p.POST("/new-mailbox", handleNewMailbox)

	p.GET("/delete-mailbox/:mbox", handleDeleteMailbox)
	p.POST("/delete-mailbox/:mbox", handleDeleteMailbox)

	p.GET("/message/:mbox/:uid", func(ctx *websrv.Context) error {
		return handleGetPart(ctx, false)
	})
	p.GET("/message/:mbox/:uid/raw", func(ctx *websrv.Context) error {
		return handleGetPart(ctx, true)
	})

	p.GET("/login", handleLogin)
	p.POST("/login", handleLogin)

	p.GET("/logout", handleLogout)

	p.GET("/compose", handleComposeNew)
	p.POST("/compose", handleComposeNew)

	p.POST("/compose/attachment", handleComposeAttachment)
	p.POST("/compose/attachment/:uuid/remove", handleCancelAttachment)

	p.GET("/message/:mbox/:uid/reply", handleReply)
	p.POST("/message/:mbox/:uid/reply", handleReply)

	p.GET("/message/:mbox/:uid/forward", handleForward)
	p.POST("/message/:mbox/:uid/forward", handleForward)

	p.GET("/message/:mbox/:uid/edit", handleEdit)
	p.POST("/message/:mbox/:uid/edit", handleEdit)

	p.POST("/message/:mbox/move", handleMove)

	p.POST("/message/:mbox/delete", handleDelete)

	p.POST("/message/:mbox/flag", handleSetFlags)

	p.GET("/settings", handleSettings)
	p.POST("/settings", handleSettings)
}

type IMAPBaseRenderData struct {
	websrv.BaseRenderData
	CategorizedMailboxes CategorizedMailboxes
	Mailboxes            []MailboxInfo
	Mailbox              *MailboxStatus
	Inbox                *MailboxStatus
	Subscriptions        map[string]*MailboxStatus
}

type MailboxRenderData struct {
	IMAPBaseRenderData
	Messages           []IMAPMessage
	PrevPage, NextPage int
	Query              string
}

type MailboxDetails struct {
	Info   *MailboxInfo
	Status *MailboxStatus
}

// Organizes mailboxes into common/uncommon categories
type CategorizedMailboxes struct {
	Common struct {
		Inbox   *MailboxDetails
		Drafts  *MailboxDetails
		Sent    *MailboxDetails
		Junk    *MailboxDetails
		Trash   *MailboxDetails
		Archive *MailboxDetails
	}
	Additional []MailboxDetails
}

func (cc *CategorizedMailboxes) Append(mi MailboxInfo, status *MailboxStatus) {
	details := &MailboxDetails{
		Info:   &mi,
		Status: status,
	}
	if name := mi.Name; name == "INBOX" {
		cc.Common.Inbox = details
	} else if name == "Drafts" {
		cc.Common.Drafts = details
	} else if name == "Sent" {
		cc.Common.Sent = details
	} else if name == "Junk" {
		cc.Common.Junk = details
	} else if name == "Trash" {
		cc.Common.Trash = details
	} else if name == "Archive" {
		cc.Common.Archive = details
	} else {
		cc.Additional = append(cc.Additional, *details)
	}
}

func newIMAPBaseRenderData(ctx *websrv.Context,
	base *websrv.BaseRenderData) (*IMAPBaseRenderData, error) {

	mboxName, err := url.PathUnescape(ctx.Param("mbox"))
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusBadRequest, err)
	}

	settings, err := LoadSettings(ctx.Session.Store())
	if err != nil {
		return nil, fmt.Errorf("failed to load settings: %v", err)
	}

	subscriptions := make(map[string]*MailboxStatus)
	var mailboxes []MailboxInfo
	var active, inbox *MailboxStatus
	err = ctx.Session.DoIMAP(func(c *imapclient.Client) error {
		var err error
		if mailboxes, err = listMailboxes(c); err != nil {
			return err
		}

		if mboxName != "" {
			if active, err = getMailboxStatus(c, mboxName); err != nil {
				return echo.NewHTTPError(http.StatusNotFound, err)
			}
		}

		if mboxName == "INBOX" {
			inbox = active
		} else {
			if inbox, err = getMailboxStatus(c, "INBOX"); err != nil {
				return err
			}
		}

		for _, sub := range settings.Subscriptions {
			if status, err := getMailboxStatus(c, sub); err != nil {
				return err
			} else {
				subscriptions[sub] = status
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	var categorized CategorizedMailboxes

	for i := range mailboxes {
		// Populate unseen & active states
		if active != nil && mailboxes[i].Name == active.Name {
			mailboxes[i].Unseen = int(active.Unseen)
			mailboxes[i].Total = int(active.Messages)
			mailboxes[i].Active = true
		}
		if mailboxes[i].Name == inbox.Name {
			mailboxes[i].Unseen = int(inbox.Unseen)
			mailboxes[i].Total = int(inbox.Messages)
		}

		categorized.Append(mailboxes[i], subscriptions[mailboxes[i].Name])
	}

	return &IMAPBaseRenderData{
		BaseRenderData:       *base,
		CategorizedMailboxes: categorized,
		Mailboxes:            mailboxes,
		Inbox:                inbox,
		Mailbox:              active,
		Subscriptions:        subscriptions,
	}, nil
}

func handleGetMailbox(ctx *websrv.Context) error {
	ibase, err := newIMAPBaseRenderData(ctx, websrv.NewBaseRenderData(ctx))
	if err != nil {
		return err
	}

	mbox := ibase.Mailbox
	title := mbox.Name
	if title == "INBOX" {
		title = "Inbox"
	}
	if mbox.Unseen > 0 {
		title = fmt.Sprintf("(%d) %s", mbox.Unseen, title)
	}
	ibase.BaseRenderData.WithTitle(title)

	page := 0
	if pageStr := ctx.QueryParam("page"); pageStr != "" {
		var err error
		if page, err = strconv.Atoi(pageStr); err != nil || page < 0 {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid page index")
		}
	}

	settings, err := LoadSettings(ctx.Session.Store())
	if err != nil {
		return err
	}
	messagesPerPage := settings.MessagesPerPage

	query := ctx.QueryParam("query")

	var (
		msgs  []IMAPMessage
		total int
	)
	err = ctx.Session.DoIMAP(func(c *imapclient.Client) error {
		var err error
		if query != "" {
			msgs, total, err = searchMessages(c, mbox.Name, query, page, messagesPerPage)
		} else {
			msgs, err = listMessages(c, mbox, page, messagesPerPage)
			total = int(mbox.Messages)
		}
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}

	prevPage, nextPage := -1, -1
	if page > 0 {
		prevPage = page - 1
	}
	if (page+1)*messagesPerPage < total {
		nextPage = page + 1
	}

	return ctx.Render(http.StatusOK, "mailbox.html", &MailboxRenderData{
		IMAPBaseRenderData: *ibase,
		Messages:           msgs,
		PrevPage:           prevPage,
		NextPage:           nextPage,
		Query:              query,
	})
}

type NewMailboxRenderData struct {
	IMAPBaseRenderData
	Error string
}

func handleNewMailbox(ctx *websrv.Context) error {
	ibase, err := newIMAPBaseRenderData(ctx, websrv.NewBaseRenderData(ctx))
	if err != nil {
		return err
	}
	ibase.BaseRenderData.WithTitle("Create new folder")

	if ctx.Request().Method == http.MethodPost {
		name := ctx.FormValue("name")
		if name == "" {
			return ctx.Render(http.StatusOK, "new-mailbox.html", &NewMailboxRenderData{
				IMAPBaseRenderData: *ibase,
				Error:              "Name is required",
			})
		}

		err := ctx.Session.DoIMAP(func(c *imapclient.Client) error {
			return c.Create(name)
		})

		if err != nil {
			return ctx.Render(http.StatusOK, "new-mailbox.html", &NewMailboxRenderData{
				IMAPBaseRenderData: *ibase,
				Error:              err.Error(),
			})
		}

		return ctx.Redirect(http.StatusFound, fmt.Sprintf("/mailbox/%s", url.PathEscape(name)))
	}

	return ctx.Render(http.StatusOK, "new-mailbox.html", &NewMailboxRenderData{
		IMAPBaseRenderData: *ibase,
		Error:              "",
	})
}

func handleDeleteMailbox(ctx *websrv.Context) error {
	ibase, err := newIMAPBaseRenderData(ctx, websrv.NewBaseRenderData(ctx))
	if err != nil {
		return err
	}

	mbox := ibase.Mailbox
	ibase.BaseRenderData.WithTitle("Delete folder '" + mbox.Name + "'")

	if ctx.Request().Method == http.MethodPost {
		ctx.Session.DoIMAP(func(c *imapclient.Client) error {
			return c.Delete(mbox.Name)
		})
		ctx.Session.PutNotice("Mailbox deleted.")
		return ctx.Redirect(http.StatusFound, "/mailbox/INBOX")
	}

	return ctx.Render(http.StatusOK, "delete-mailbox.html", ibase)
}

func handleLogin(ctx *websrv.Context) error {
	username := ctx.FormValue("username")
	password := ctx.FormValue("password")
	remember := ctx.FormValue("remember-me")

	renderData := struct {
		websrv.BaseRenderData
		CanRememberMe bool
	}{
		BaseRenderData: *websrv.NewBaseRenderData(ctx),
		CanRememberMe:  ctx.Server.Config.Security.LoginKey != nil,
	}

	if username == "" && password == "" {
		username, password = ctx.GetLoginToken()
	}

	if username != "" && password != "" {
		s, err := ctx.Server.Sessions.Put(username, password)
		if err != nil {
			if _, ok := err.(websrv.AuthError); ok {
				renderData.BaseRenderData.GlobalData.Notice = "Failed to login!"
				return ctx.Render(http.StatusUnauthorized, "login.html", &renderData)
			}
			return fmt.Errorf("failed to put connection in pool: %v", err)
		}
		ctx.SetSession(s)

		ctx.SetSessionLoginToken(username, password)
		if remember == "on" {
			ctx.SetRememberLoginToken(username, password)
		}

		// Request has the original redirected method and body.
		if path := ctx.QueryParam("next"); path != "" && path[0] == '/' && path != "/login" {
			return ctx.Redirect(http.StatusTemporaryRedirect, path)
		}
		return ctx.Redirect(http.StatusFound, "/mailbox/INBOX")
	}

	return ctx.Render(http.StatusOK, "login.html", &renderData)
}

func handleLogout(ctx *websrv.Context) error {
	ctx.Session.Close()
	ctx.SetSession(nil)
	ctx.SetSessionLoginToken("", "")
	ctx.SetRememberLoginToken("", "")
	return ctx.Redirect(http.StatusFound, "/login")
}

type MessageRenderData struct {
	IMAPBaseRenderData
	Message     *IMAPMessage
	Part        *IMAPPartNode
	View        interface{}
	MailboxPage int
	Flags       map[string]bool
}

func handleGetPart(ctx *websrv.Context, raw bool) error {
	_, uid, err := parseMboxAndUid(ctx.Param("mbox"), ctx.Param("uid"))
	if err != nil {
		return err
	}
	ibase, err := newIMAPBaseRenderData(ctx, websrv.NewBaseRenderData(ctx))
	if err != nil {
		return err
	}
	mbox := ibase.Mailbox

	partPath, err := parsePartPath(ctx.QueryParam("part"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	settings, err := LoadSettings(ctx.Session.Store())
	if err != nil {
		return err
	}
	messagesPerPage := settings.MessagesPerPage
	loc, err := time.LoadLocation(settings.Timezone)
	if err != nil {
		return fmt.Errorf("failed to load location: %v", err)
	}

	var msg *IMAPMessage
	var part *message.Entity
	err = ctx.Session.DoIMAP(func(c *imapclient.Client) error {
		var err error
		if msg, part, err = getMessagePart(c, mbox.Name, uid, partPath); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}

	msg.Envelope.Date = msg.Envelope.Date.In(loc)

	mimeType, _, err := part.Header.ContentType()
	if err != nil {
		return fmt.Errorf("failed to parse part Content-Type: %v", err)
	}
	if len(partPath) == 0 {
		if ctx.QueryParam("plain") == "1" {
			mimeType = "text/plain"
		} else {
			mimeType = "message/rfc822"
		}
	}

	if raw {
		ctx.Response().Header().Set("Content-Type", mimeType)

		disp, dispParams, _ := part.Header.ContentDisposition()
		filename := dispParams["filename"]
		if len(partPath) == 0 {
			filename = msg.Envelope.Subject + ".eml"
		}

		// TODO: set Content-Length if possible

		// Be careful not to serve types like text/html as inline
		if !strings.EqualFold(mimeType, "text/plain") || strings.EqualFold(disp, "attachment") {
			dispParams := make(map[string]string)
			if filename != "" {
				dispParams["filename"] = filename
			}
			disp := mime.FormatMediaType("attachment", dispParams)
			ctx.Response().Header().Set("Content-Disposition", disp)
		}

		if len(partPath) == 0 {
			return part.WriteTo(ctx.Response())
		}
		return ctx.Stream(http.StatusOK, mimeType, part.Body)
	}

	view, err := viewMessagePart(ctx, msg, part)
	if err == ErrViewUnsupported {
		view = nil
	}

	if view != nil && ctx.QueryParam("src") == "1" {
		tpl := template.Must(template.New("").Parse("{{.}}"))
		var buf bytes.Buffer
		err = tpl.Execute(&buf, view)
		if err != nil {
			return fmt.Errorf("failed to execute template for src of view: %v", err)
		}
		return ctx.HTML(http.StatusOK, buf.String())
	}

	flags := make(map[string]bool)
	for _, f := range mbox.PermanentFlags {
		f = imap.CanonicalFlag(f)
		if f == imap.TryCreateFlag {
			continue
		}
		flags[f] = msg.HasFlag(f)
	}

	ibase.BaseRenderData.WithTitle(msg.Envelope.Subject)

	return ctx.Render(http.StatusOK, "message.html", &MessageRenderData{
		IMAPBaseRenderData: *ibase,
		Message:            msg,
		Part:               msg.PartByPath(partPath),
		View:               view,
		MailboxPage:        int(mbox.Messages-msg.SeqNum) / messagesPerPage,
		Flags:              flags,
	})
}

type ComposeRenderData struct {
	IMAPBaseRenderData
	Message *OutgoingMessage
}

type messagePath struct {
	Mailbox string
	Uid     uint32
}

type composeOptions struct {
	Draft     *messagePath
	Forward   *messagePath
	InReplyTo *messagePath
}

// Send message, append it to the Sent mailbox, mark the original message as
// answered, delete from the Draft mailbox
func submitCompose(ctx *websrv.Context, msg *OutgoingMessage, options *composeOptions) error {
	draft := options.Draft
	if draft == nil {
		return fmt.Errorf("expected a draft message")
	}

	err := ctx.Session.DoSMTP(func(c *smtp.Client) error {
		return sendMessage(c, msg)
	})
	if err != nil {
		if _, ok := err.(websrv.AuthError); ok {
			return echo.NewHTTPError(http.StatusForbidden, err)
		}
		ctx.Session.PutNotice(fmt.Sprintf("Failed to send message: %v", err))
		return ctx.Redirect(http.StatusFound, fmt.Sprintf(
			"/message/%s/%d/edit?part=1", draft.Mailbox, draft.Uid))
	}

	if inReplyTo := options.InReplyTo; inReplyTo != nil {
		err = ctx.Session.DoIMAP(func(c *imapclient.Client) error {
			return markMessageAnswered(c, inReplyTo.Mailbox, inReplyTo.Uid)
		})
		if err != nil {
			return fmt.Errorf("failed to mark original message as answered: %v", err)
		}
	}

	err = ctx.Session.DoIMAP(func(c *imapclient.Client) error {
		if _, err := appendMessage(c, msg, mailboxSent); err != nil {
			return err
		}
		if err := deleteMessage(c, draft.Mailbox, draft.Uid); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to save message to Sent mailbox: %v", err)
	}

	ctx.Session.PutNotice("Message sent.")
	return ctx.Redirect(http.StatusFound, "/mailbox/INBOX")
}

func handleCompose(ctx *websrv.Context, msg *OutgoingMessage, options *composeOptions) error {
	ibase, err := newIMAPBaseRenderData(ctx, websrv.NewBaseRenderData(ctx))
	if err != nil {
		return err
	}

	if msg.From == "" && strings.ContainsRune(ctx.Session.Username(), '@') {
		settings, err := LoadSettings(ctx.Session.Store())
		if err != nil {
			return err
		}
		msg.From = formatAddress(&mail.Address{
			Name:    settings.From,
			Address: ctx.Session.Username(),
		})
	}
	if msg.From == "" {
		return fmt.Errorf("please login with a valid email address, From couldn't be empty")
	}

	if ctx.Request().Method == http.MethodPost {
		formParams, err := ctx.FormParams()
		if err != nil {
			return fmt.Errorf("failed to parse form: %v", err)
		}
		_, saveAsDraft := formParams["save_as_draft"]

		msg.From = ctx.FormValue("from")
		msg.To = parseStringList(ctx.FormValue("to"))
		msg.Cc = parseStringList(ctx.FormValue("cc"))
		msg.Bcc = parseStringList(ctx.FormValue("bcc"))
		msg.Subject = ctx.FormValue("subject")
		msg.Text = ctx.FormValue("text")
		msg.InReplyTo = ctx.FormValue("in_reply_to")
		msg.MessageID = ctx.FormValue("message_id")

		if msg.From == "" {
			return fmt.Errorf("the From is not set")
		}

		form, err := ctx.MultipartForm()
		if err != nil {
			return fmt.Errorf("failed to get multipart form: %v", err)
		}

		// Fetch previous attachments from original message
		var original *messagePath
		if options.Draft != nil {
			original = options.Draft
		} else if options.Forward != nil {
			original = options.Forward
		}
		if original != nil {
			for _, s := range form.Value["prev_attachments"] {
				path, err := parsePartPath(s)
				if err != nil {
					return fmt.Errorf("failed to parse original attachment path: %v", err)
				}

				var part *message.Entity
				err = ctx.Session.DoIMAP(func(c *imapclient.Client) error {
					var err error
					_, part, err = getMessagePart(c, original.Mailbox, original.Uid, path)
					return err
				})
				if err != nil {
					return fmt.Errorf("failed to fetch attachment from original message: %v", err)
				}

				var buf bytes.Buffer
				if _, err := io.Copy(&buf, part.Body); err != nil {
					return fmt.Errorf("failed to copy attachment from original message: %v", err)
				}

				h := mail.AttachmentHeader{Header: part.Header}
				mimeType, _, _ := h.ContentType()
				filename, _ := h.Filename()
				msg.Attachments = append(msg.Attachments, &imapAttachment{
					Mailbox: original.Mailbox,
					Uid:     original.Uid,
					Node: &IMAPPartNode{
						Path:     path,
						MIMEType: mimeType,
						Filename: filename,
					},
					Body: buf.Bytes(),
				})
			}
		} else if len(form.Value["prev_attachments"]) > 0 {
			return fmt.Errorf("previous attachments specified but no original message available")
		}

		for _, fh := range form.File["attachments"] {
			msg.Attachments = append(msg.Attachments, &formAttachment{fh})
		}

		uuids := ctx.FormValue("attachment-uuids")
		for _, uuid := range strings.Split(uuids, ",") {
			if uuid == "" {
				continue
			}

			attachment := ctx.Session.PopAttachment(uuid)
			if attachment == nil {
				return fmt.Errorf("unable to retrieve message attachment %s from session", uuid)
			}
			msg.Attachments = append(msg.Attachments,
				&formAttachment{attachment.File})
			defer attachment.Form.RemoveAll()
		}

		// Save as draft before sending to prevent data loss
		var draft *messagePath
		err = ctx.Session.DoIMAP(func(c *imapclient.Client) error {
			drafts, err := appendMessage(c, msg, mailboxDrafts)
			if err != nil {
				return err
			}

			if draft := options.Draft; draft != nil {
				if err := deleteMessage(c, draft.Mailbox, draft.Uid); err != nil {
					return err
				}
			}

			if err := ensureMailboxSelected(c, drafts.Name); err != nil {
				return err
			}

			criteria := &imap.SearchCriteria{
				Header: make(textproto.MIMEHeader),
			}
			criteria.Header.Add("Message-Id", msg.MessageID)
			uids, err := c.UidSearch(criteria)
			if err != nil {
				return err
			}
			if len(uids) != 1 {
				panic(fmt.Errorf("duplicate message ID"))
			}

			draft = &messagePath{Mailbox: drafts.Name, Uid: uids[0]}
			return nil
		})
		if err != nil {
			return fmt.Errorf("failed to save message to Draft mailbox: %v", err)
		}

		if saveAsDraft {
			ctx.Session.PutNotice("Message saved as draft.")
			return ctx.Redirect(http.StatusFound, fmt.Sprintf(
				"/message/%s/%d/edit?part=1", draft.Mailbox, draft.Uid))
		} else {
			options.Draft = draft
			return submitCompose(ctx, msg, options)
		}
	}

	return ctx.Render(http.StatusOK, "compose.html", &ComposeRenderData{
		IMAPBaseRenderData: *ibase,
		Message:            msg,
	})
}

func handleComposeNew(ctx *websrv.Context) error {
	text := ctx.QueryParam("body")
	settings, err := LoadSettings(ctx.Session.Store())
	if err != nil {
		return nil
	}
	if text == "" && settings.Signature != "" {
		text = "\n\n\n-- \n" + settings.Signature
	}

	// These are common mailto URL query parameters
	var hdr mail.Header
	hdr.GenerateMessageID()
	mid, _ := hdr.MessageID()
	return handleCompose(ctx, &OutgoingMessage{
		To:        strings.Split(ctx.QueryParam("to"), ","),
		Cc:        strings.Split(ctx.QueryParam("cc"), ","),
		Bcc:       strings.Split(ctx.QueryParam("bcc"), ","),
		Subject:   ctx.QueryParam("subject"),
		MessageID: "<" + mid + ">",
		InReplyTo: ctx.QueryParam("in-reply-to"),
		Text:      text,
	}, &composeOptions{})
}

func handleComposeAttachment(ctx *websrv.Context) error {
	reader, err := ctx.Request().MultipartReader()
	if err != nil {
		return ctx.JSON(http.StatusBadRequest, map[string]string{
			"error": "Invalid request",
		})
	}
	form, err := reader.ReadForm(32 << 20) // 32 MB
	if err != nil {
		return ctx.JSON(http.StatusBadRequest, map[string]string{
			"error": "Invalid request",
		})
	}

	var uuids []string
	for _, fh := range form.File["attachments"] {
		uuid, err := ctx.Session.PutAttachment(fh, form)
		if err == websrv.ErrAttachmentCacheSize {
			form.RemoveAll()
			return ctx.JSON(http.StatusBadRequest, map[string]string{
				"error": "Your attachments exceed the maximum file size. Remove some and try again.",
			})
		} else if err != nil {
			form.RemoveAll()
			ctx.Logger().Printf("PutAttachment: %v\n", err)
			return ctx.JSON(http.StatusBadRequest, map[string]string{
				"error": "failed to store attachment",
			})
		}
		uuids = append(uuids, uuid)
	}

	return ctx.JSON(http.StatusOK, &uuids)
}

func handleCancelAttachment(ctx *websrv.Context) error {
	uuid := ctx.Param("uuid")
	a := ctx.Session.PopAttachment(uuid)
	if a != nil {
		a.Form.RemoveAll()
	}
	return ctx.JSON(http.StatusOK, nil)
}

// Taken with modifications from:
// https://git.sr.ht/~rjarry/aerc/tree/master/item/lib/format/format.go
// If the address's name contains non-ASCII characters
// the name will be quoted but not encoded.
func formatAddress(a *mail.Address) string {
	if a.Name != "" {
		if atom.MatchString(a.Name) {
			return fmt.Sprintf("%s <%s>", a.Name, a.Address)
		} else {
			return fmt.Sprintf("\"%s\" <%s>",
				strings.ReplaceAll(a.Name, "\"", "'"), a.Address)
		}
	} else {
		return a.Address
	}
}

// Atom as defined in RFC 5322 3.2.3.
var atom *regexp.Regexp = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+-/=?^_`{|}~]+$")

func formatIMAPAddressList(addrs []*imap.Address) []string {
	l := make([]string, len(addrs))
	for i, addr := range addrs {
		l[i] = formatAddress(&mail.Address{
			Name:    addr.PersonalName,
			Address: addr.Address(),
		})
	}
	return l
}

func handleReply(ctx *websrv.Context) error {
	var inReplyToPath messagePath
	var err error
	inReplyToPath.Mailbox, inReplyToPath.Uid, err = parseMboxAndUid(ctx.Param("mbox"), ctx.Param("uid"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	var msg OutgoingMessage
	if ctx.Request().Method == http.MethodGet {
		// Populate fields from original message
		partPath, err := parsePartPath(ctx.QueryParam("part"))
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err)
		}

		var inReplyTo *IMAPMessage
		var part *message.Entity
		err = ctx.Session.DoIMAP(func(c *imapclient.Client) error {
			var err error
			inReplyTo, part, err = getMessagePart(c, inReplyToPath.Mailbox, inReplyToPath.Uid, partPath)
			return err
		})
		if err != nil {
			return err
		}

		mimeType, _, err := part.Header.ContentType()
		if err != nil {
			return fmt.Errorf("failed to parse part Content-Type: %v", err)
		}

		if mimeType == "text/plain" {
			msg.Text, err = quote(part.Body)
			if err != nil {
				return err
			}
		} else if mimeType == "text/html" {
			text, err := html2text.FromReader(part.Body, html2text.Options{})
			if err != nil {
				return err
			}
			msg.Text, err = quote(strings.NewReader(text))
			if err != nil {
				return nil
			}
		} else {
			err := fmt.Errorf("cannot forward %q part", mimeType)
			return echo.NewHTTPError(http.StatusBadRequest, err)
		}

		var hdr mail.Header
		hdr.GenerateMessageID()
		mid, _ := hdr.MessageID()
		msg.MessageID = "<" + mid + ">"
		msg.InReplyTo = inReplyTo.Envelope.MessageId
		// TODO: populate From from known user addresses and inReplyTo.Envelope.To
		replyTo := inReplyTo.Envelope.ReplyTo
		if len(replyTo) == 0 {
			replyTo = inReplyTo.Envelope.From
		}
		msg.To = formatIMAPAddressList(replyTo)
		msg.Subject = inReplyTo.Envelope.Subject
		if !strings.HasPrefix(strings.ToLower(msg.Subject), "re:") {
			msg.Subject = "Re: " + msg.Subject
		}
	}

	return handleCompose(ctx, &msg, &composeOptions{InReplyTo: &inReplyToPath})
}

func handleForward(ctx *websrv.Context) error {
	var sourcePath messagePath
	var err error
	sourcePath.Mailbox, sourcePath.Uid, err = parseMboxAndUid(ctx.Param("mbox"), ctx.Param("uid"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	var msg OutgoingMessage
	if ctx.Request().Method == http.MethodGet {
		// Populate fields from original message
		partPath, err := parsePartPath(ctx.QueryParam("part"))
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err)
		}

		var source *IMAPMessage
		var part *message.Entity
		err = ctx.Session.DoIMAP(func(c *imapclient.Client) error {
			var err error
			source, part, err = getMessagePart(c, sourcePath.Mailbox, sourcePath.Uid, partPath)
			return err
		})
		if err != nil {
			return err
		}

		mimeType, _, err := part.Header.ContentType()
		if err != nil {
			return fmt.Errorf("failed to parse part Content-Type: %v", err)
		}

		if mimeType == "text/plain" {
			msg.Text, err = quote(part.Body)
			if err != nil {
				return err
			}
		} else if mimeType == "text/html" {
			msg.Text, err = html2text.FromReader(part.Body, html2text.Options{})
			if err != nil {
				return err
			}
		} else {
			err := fmt.Errorf("cannot forward %q part", mimeType)
			return echo.NewHTTPError(http.StatusBadRequest, err)
		}

		var hdr mail.Header
		hdr.GenerateMessageID()
		mid, _ := hdr.MessageID()
		msg.MessageID = "<" + mid + ">"
		msg.Subject = source.Envelope.Subject
		if !strings.HasPrefix(strings.ToLower(msg.Subject), "fwd:") &&
			!strings.HasPrefix(strings.ToLower(msg.Subject), "fw:") {
			msg.Subject = "Fwd: " + msg.Subject
		}
		msg.InReplyTo = source.Envelope.InReplyTo

		attachments := source.Attachments()
		for i := range attachments {
			// No need to populate attachment body here, we just need the
			// metadata
			msg.Attachments = append(msg.Attachments, &imapAttachment{
				Mailbox: sourcePath.Mailbox,
				Uid:     sourcePath.Uid,
				Node:    &attachments[i],
			})
		}
	}

	return handleCompose(ctx, &msg, &composeOptions{Forward: &sourcePath})
}

func handleEdit(ctx *websrv.Context) error {
	var sourcePath messagePath
	var err error
	sourcePath.Mailbox, sourcePath.Uid, err = parseMboxAndUid(ctx.Param("mbox"), ctx.Param("uid"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	// TODO: somehow get the path to the In-Reply-To message (with a search?)

	var msg OutgoingMessage
	if ctx.Request().Method == http.MethodGet {
		// Populate fields from source message
		partPath, err := parsePartPath(ctx.QueryParam("part"))
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err)
		}

		var source *IMAPMessage
		var part *message.Entity
		err = ctx.Session.DoIMAP(func(c *imapclient.Client) error {
			var err error
			source, part, err = getMessagePart(c, sourcePath.Mailbox, sourcePath.Uid, partPath)
			return err
		})
		if err != nil {
			return err
		}

		mimeType, _, err := part.Header.ContentType()
		if err != nil {
			return fmt.Errorf("failed to parse part Content-Type: %v", err)
		}

		if !strings.EqualFold(mimeType, "text/plain") {
			err := fmt.Errorf("cannot edit %q part", mimeType)
			return echo.NewHTTPError(http.StatusBadRequest, err)
		}

		b, err := io.ReadAll(part.Body)
		if err != nil {
			return fmt.Errorf("failed to read part body: %v", err)
		}
		msg.Text = string(b)

		if len(source.Envelope.From) > 0 {
			msg.From = source.Envelope.From[0].Address()
		}
		msg.To = formatIMAPAddressList(source.Envelope.To)
		msg.Cc = formatIMAPAddressList(source.Envelope.Cc)
		msg.Bcc = formatIMAPAddressList(source.Envelope.Bcc)
		msg.Subject = source.Envelope.Subject
		msg.InReplyTo = source.Envelope.InReplyTo
		msg.MessageID = source.Envelope.MessageId

		attachments := source.Attachments()
		for i := range attachments {
			// No need to populate attachment body here, we just need the
			// metadata
			msg.Attachments = append(msg.Attachments, &imapAttachment{
				Mailbox: sourcePath.Mailbox,
				Uid:     sourcePath.Uid,
				Node:    &attachments[i],
			})
		}
	}

	return handleCompose(ctx, &msg, &composeOptions{Draft: &sourcePath})
}

func formOrQueryParam(ctx *websrv.Context, k string) string {
	if v := ctx.FormValue(k); v != "" {
		return v
	}
	return ctx.QueryParam(k)
}

func handleMove(ctx *websrv.Context) error {
	mboxName, err := url.PathUnescape(ctx.Param("mbox"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	formParams, err := ctx.FormParams()
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}
	uids, err := parseUidList(formParams["uids"])
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	if len(uids) == 0 {
		ctx.Session.PutNotice("No messages selected.")
		return ctx.Redirect(http.StatusFound, fmt.Sprintf("/mailbox/%v", url.PathEscape(mboxName)))
	}

	to := formOrQueryParam(ctx, "to")
	if to == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "missing 'to' form parameter")
	}

	err = ctx.Session.DoIMAP(func(c *imapclient.Client) error {
		mc := imapmove.NewClient(c)

		if err := ensureMailboxSelected(c, mboxName); err != nil {
			return err
		}

		var seqSet imap.SeqSet
		seqSet.AddNum(uids...)
		if err := mc.UidMoveWithFallback(&seqSet, to); err != nil {
			return fmt.Errorf("failed to move message: %v", err)
		}

		// TODO: get the UID of the message in the destination mailbox with UIDPLUS
		return nil
	})
	if err != nil {
		return err
	}

	ctx.Session.PutNotice("Message(s) moved.")
	if path := formOrQueryParam(ctx, "next"); path != "" {
		return ctx.Redirect(http.StatusFound, path)
	}
	return ctx.Redirect(http.StatusFound, fmt.Sprintf("/mailbox/%v", url.PathEscape(mboxName)))
}

func handleDelete(ctx *websrv.Context) error {
	mboxName, err := url.PathUnescape(ctx.Param("mbox"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	formParams, err := ctx.FormParams()
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}
	uids, err := parseUidList(formParams["uids"])
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	if len(uids) == 0 {
		ctx.Session.PutNotice("No messages selected.")
		return ctx.Redirect(http.StatusFound, fmt.Sprintf("/mailbox/%v", url.PathEscape(mboxName)))
	}

	err = ctx.Session.DoIMAP(func(c *imapclient.Client) error {
		if err := ensureMailboxSelected(c, mboxName); err != nil {
			return err
		}

		var seqSet imap.SeqSet
		seqSet.AddNum(uids...)

		item := imap.FormatFlagsOp(imap.AddFlags, true)
		flags := []interface{}{imap.DeletedFlag}
		if err := c.UidStore(&seqSet, item, flags, nil); err != nil {
			return fmt.Errorf("failed to add deleted flag: %v", err)
		}

		if err := c.Expunge(nil); err != nil {
			return fmt.Errorf("failed to expunge mailbox: %v", err)
		}

		// Deleting a message invalidates our cached message count
		// TODO: listen to async updates instead
		if _, err := c.Select(mboxName, false); err != nil {
			return fmt.Errorf("failed to select mailbox: %v", err)
		}

		return nil
	})
	if err != nil {
		return err
	}

	ctx.Session.PutNotice("Message(s) deleted.")
	if path := formOrQueryParam(ctx, "next"); path != "" {
		return ctx.Redirect(http.StatusFound, path)
	}
	return ctx.Redirect(http.StatusFound, fmt.Sprintf("/mailbox/%v", url.PathEscape(mboxName)))
}

func handleSetFlags(ctx *websrv.Context) error {
	mboxName, err := url.PathUnescape(ctx.Param("mbox"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	formParams, err := ctx.FormParams()
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	uids, err := parseUidList(formParams["uids"])
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	flags, ok := formParams["flags"]
	if !ok {
		flagsStr := ctx.QueryParam("to")
		if flagsStr == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "missing 'flags' form parameter")
		}
		flags = strings.Fields(flagsStr)
	}

	actionStr := ctx.FormValue("action")
	if actionStr == "" {
		actionStr = ctx.QueryParam("action")
	}

	var op imap.FlagsOp
	switch actionStr {
	case "", "set":
		op = imap.SetFlags
	case "add":
		op = imap.AddFlags
	case "remove":
		op = imap.RemoveFlags
	default:
		return echo.NewHTTPError(http.StatusBadRequest, "invalid 'action' value")
	}

	err = ctx.Session.DoIMAP(func(c *imapclient.Client) error {
		if err := ensureMailboxSelected(c, mboxName); err != nil {
			return err
		}

		var seqSet imap.SeqSet
		seqSet.AddNum(uids...)

		storeItems := make([]interface{}, len(flags))
		for i, f := range flags {
			storeItems[i] = f
		}

		item := imap.FormatFlagsOp(op, true)
		if err := c.UidStore(&seqSet, item, storeItems, nil); err != nil {
			return fmt.Errorf("failed to add deleted flag: %v", err)
		}

		return nil
	})
	if err != nil {
		return err
	}

	if path := formOrQueryParam(ctx, "next"); path != "" {
		return ctx.Redirect(http.StatusFound, path)
	}
	if len(uids) != 1 || (op == imap.RemoveFlags && len(flags) == 1 && flags[0] == imap.SeenFlag) {
		// Redirecting to the message view would mark the message as read again
		return ctx.Redirect(http.StatusFound, fmt.Sprintf("/mailbox/%v", url.PathEscape(mboxName)))
	}
	return ctx.Redirect(http.StatusFound, fmt.Sprintf("/message/%v/%v", url.PathEscape(mboxName), uids[0]))
}

const settingsKey = "base.settings"
const maxMessagesPerPage = 100

type Settings struct {
	MessagesPerPage int
	Signature       string
	From            string
	Subscriptions   []string
	Timezone        string
}

func LoadSettings(s websrv.Store) (*Settings, error) {
	settings := &Settings{
		MessagesPerPage: 50,
	}
	if err := s.Get(settingsKey, settings); err != nil && err != websrv.ErrNoStoreEntry {
		return nil, err
	}
	if err := settings.check(); err != nil {
		return nil, err
	}
	return settings, nil
}

func (s *Settings) check() error {
	if s.MessagesPerPage <= 0 || s.MessagesPerPage > maxMessagesPerPage {
		return fmt.Errorf("messages per page out of bounds: %v", s.MessagesPerPage)
	}
	if len(s.Signature) > 2048 {
		return fmt.Errorf("signature must be 2048 characters or fewer")
	}
	if len(s.From) > 512 {
		return fmt.Errorf("full name must be 512 characters or fewer")
	}
	return nil
}

type SettingsRenderData struct {
	websrv.BaseRenderData
	Mailboxes     []MailboxInfo
	Settings      *Settings
	Subscriptions Subscriptions
	Regions       []string
	Timezones     map[string][]string
}

type Subscriptions []string

func (s Subscriptions) Has(sub string) bool {
	for _, cand := range s {
		if cand == sub {
			return true
		}
	}
	return false
}

func handleSettings(ctx *websrv.Context) error {
	settings, err := LoadSettings(ctx.Session.Store())
	if err != nil {
		return fmt.Errorf("failed to load settings: %v", err)
	}

	var mailboxes []MailboxInfo
	err = ctx.Session.DoIMAP(func(c *imapclient.Client) error {
		mailboxes, err = listMailboxes(c)
		return err
	})
	if err != nil {
		return err
	}

	regions := []string{
		"Africa", "America", "Antarctica", "Asia", "Atlantic", "Australia",
		"Europe", "Indian", "Pacific",
	}
	timezones := make(map[string][]string)
	for zone := range timezone.New().TzInfos() {
		region := strings.Split(zone, "/")[0]
		timezones[region] = append(timezones[region], zone)
	}

	for _, zones := range timezones {
		sort.Strings(zones)
	}

	if ctx.Request().Method == http.MethodPost {
		settings.MessagesPerPage, err = strconv.Atoi(ctx.FormValue("messages_per_page"))
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid messages per page: %v", err)
		}
		settings.Signature = ctx.FormValue("signature")
		settings.From = ctx.FormValue("from")
		settings.Timezone = ctx.FormValue("timezones")

		params, err := ctx.FormParams()
		if err != nil {
			return err
		}
		settings.Subscriptions = params["subscriptions"]

		if err := settings.check(); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err)
		}
		if err := ctx.Session.Store().Put(settingsKey, settings); err != nil {
			return fmt.Errorf("failed to save settings: %v", err)
		}

		return ctx.Redirect(http.StatusFound, "/mailbox/INBOX")
	}

	return ctx.Render(http.StatusOK, "settings.html", &SettingsRenderData{
		BaseRenderData: *websrv.NewBaseRenderData(ctx),
		Settings:       settings,
		Mailboxes:      mailboxes,
		Subscriptions:  Subscriptions(settings.Subscriptions),
		Regions:        regions,
		Timezones:      timezones,
	})
}
