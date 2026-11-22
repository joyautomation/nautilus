package server

import (
	"html/template"
	"io"
	"net/http"
	"strings"
	"sync"
)

// The four HTML pages a controller sends a browser to (sign-in, the OIDC
// hop, a refused confirmation, an alarm's ack link) are html/template
// pages. text/template, under it, calls reflect's MethodByName with a name
// it learns at run time, so a binary that can reach it keeps every
// exported method of every type it links: in the playground's wasm that
// was about 900 KB gzipped (#1239). So the templates are parsed by New,
// which a controller calls, into a pageSet the handlers reach only through
// an interface. NewPlayground installs none, and the playground's binary
// never reaches html/template (TestWasmLinksNoTemplateEngine,
// internal/contextmap).

// pageName is one of the HTML pages.
type pageName int

const (
	pageSignIn pageName = iota
	pageOIDCHop
	pageElevate
	pageAckLink
	pageCount
)

// pageSet renders a page. Only New installs one (htmlPages).
type pageSet interface {
	render(w io.Writer, p pageName, data any) error
}

// htmlPages is the controller's pageSet: the templates, parsed once per
// process by the first New (controllerPages).
type htmlPages [pageCount]*template.Template

// Not sync.OnceValue(newHTMLPages) in a package-level var: that runs at
// package init, which every binary links, and would pull the template
// engine back into the playground. Only New calls controllerPages.
var (
	pagesOnce   sync.Once
	pagesParsed *htmlPages
)

func controllerPages() *htmlPages {
	pagesOnce.Do(func() { pagesParsed = newHTMLPages() })
	return pagesParsed
}

func newHTMLPages() *htmlPages {
	return &htmlPages{
		pageSignIn:  template.Must(template.New("signin").Parse(signInPageHTML)),
		pageOIDCHop: template.Must(template.New("hop").Parse(oidcHopPageHTML)),
		pageElevate: template.Must(template.New("elevate").Parse(elevatePageHTML)),
		pageAckLink: template.Must(template.New("acklink").Parse(ackLinkPageHTML)),
	}
}

func (p *htmlPages) render(w io.Writer, n pageName, data any) error {
	return p[n].Execute(w, data)
}

// writePage writes the status and page p. The caller has set the page's
// headers. A server with no pages (the playground's) answers plain text
// instead: plain, the page's message.
func (s *Server) writePage(w http.ResponseWriter, status int, p pageName, data any, plain string) error {
	if s.pages == nil {
		h := w.Header()
		h.Set("Content-Type", "text/plain; charset=utf-8")
		h.Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(status)
		if strings.TrimSpace(plain) == "" {
			plain = http.StatusText(status)
		}
		_, err := io.WriteString(w, plain+"\n")
		return err
	}
	w.WriteHeader(status)
	return s.pages.render(w, p, data)
}
