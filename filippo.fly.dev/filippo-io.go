package main

import (
	"embed"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

//go:embed filippo.io
var filippoIoContent embed.FS

var goGetHtml = template.Must(template.New("go-get.html").Parse(`
{{ $repo := or .GitRepo (printf "https://github.com/FiloSottile/%s" .Name) }}
<head>
    <meta name="go-import" content="filippo.io/{{ .Name }} git {{ $repo }}">
    <meta http-equiv="refresh" content="0;URL='{{ or .Redirect $repo }}'">
<body>
    Redirecting you to the <a href="{{ or .Redirect $repo }}">project page</a>...
`))

type goGetHandler struct {
	Name     string
	GitRepo  string
	Redirect string
}

func (h goGetHandler) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	rw.Header().Set("Content-Type", "text/html; charset=UTF-8")
	httpReqs.WithLabelValues("[go-get]").Inc()
	goGetReqs.WithLabelValues(h.Name, r.URL.Query().Get("go-get")).Inc()
	goGetHtml.Execute(rw, h)
}

const mtaSTS = `version: STSv1
mode: enforce
mx: in1-smtp.messagingengine.com
mx: in2-smtp.messagingengine.com
max_age: 1209600
`

func filippoIO(mux *http.ServeMux) {
	handleFuncWithCounter(mux, "www.filippo.io/", func(rw http.ResponseWriter, r *http.Request) {
		u := &url.URL{
			Scheme: "https", Host: "filippo.io",
			Path: r.URL.Path, RawQuery: r.URL.RawQuery,
		}
		http.Redirect(rw, r, u.String(), http.StatusMovedPermanently)
	})

	plausible := &httputil.ReverseProxy{
		Director: func(r *http.Request) {
			r.Host = "plausible.io"
			r.URL.Scheme = "https"
			r.URL.Host = "plausible.io"
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			proxyErrs.Inc()
			log.Println("Plausible proxy error:", err)
			http.Error(w, "proxy error", http.StatusBadGateway)
		},
	}
	handleWithCounter(mux, "filippo.io/js/script.js", plausible)
	handleWithCounter(mux, "filippo.io/api/event", plausible)

	content, err := fs.Sub(filippoIoContent, "filippo.io")
	if err != nil {
		log.Fatal(err)
	}
	// TODO: metrics counter for which files are loaded.
	handleWithCounter(mux, "filippo.io/", http.FileServer(http.FS(content)))

	// MTA-STS for domains and subdomains
	handleFuncWithCounter(mux, "/.well-known/mta-sts.txt",
		func(rw http.ResponseWriter, r *http.Request) {
			if !strings.HasPrefix(r.Host, "mta-sts.") ||
				!strings.HasSuffix(r.Host, ".filippo.io") {
				http.Error(rw, "Not an MTA-STS domain", http.StatusNotFound)
				return
			}
			rw.Header().Set("Content-Type", "text/plain; charset=UTF-8")
			io.WriteString(rw, mtaSTS)
		})

	// git clone redirects
	handleFuncWithCounter(mux, "filippo.io/age/info/refs",
		func(rw http.ResponseWriter, r *http.Request) {
			url := "https://github.com/FiloSottile/age.git/info/refs?" + r.URL.RawQuery
			http.Redirect(rw, r, url, http.StatusFound)
		})
	handleFuncWithCounter(mux, "filippo.io/yubikey-agent/info/refs",
		func(rw http.ResponseWriter, r *http.Request) {
			url := "https://github.com/FiloSottile/yubikey-agent.git/info/refs?" + r.URL.RawQuery
			http.Redirect(rw, r, url, http.StatusFound)
		})

	// go get handlers
	mux.Handle("filippo.io/age/", goGetHandler{
		Name: "age",
	})
	mux.Handle("filippo.io/edwards25519/", goGetHandler{
		Name:     "edwards25519",
		Redirect: "https://pkg.go.dev/filippo.io/edwards25519",
	})
	mux.Handle("filippo.io/nistec/", goGetHandler{
		Name:     "nistec",
		Redirect: "https://pkg.go.dev/filippo.io/nistec",
	})
	mux.Handle("filippo.io/intermediates/", goGetHandler{
		Name:     "intermediates",
		Redirect: "https://pkg.go.dev/filippo.io/intermediates",
	})
	mux.Handle("filippo.io/cpace/", goGetHandler{
		Name:     "cpace",
		GitRepo:  "https://github.com/FiloSottile/go-cpace-ristretto255",
		Redirect: "https://pkg.go.dev/filippo.io/cpace",
	})
	mux.Handle("filippo.io/mkcert/", goGetHandler{
		Name: "mkcert",
	})
	mux.Handle("filippo.io/yubikey-agent/", goGetHandler{
		Name: "yubikey-agent",
	})
	mux.Handle("filippo.io/mostly-harmless/", goGetHandler{
		Name: "mostly-harmless",
	})

	// Miscellaneous redirects
	for path, url := range map[string]string{
		"/ticketbleed/":                         "https://filippo.io/Ticketbleed/",
		"/heartbleed/":                          "https://filippo.io/Heartbleed/",
		"/cve-2016-2107/":                       "https://filippo.io/CVE-2016-2107/",
		"/badfish/":                             "https://filippo.io/Badfish/",
		"/hitb":                                 "https://imgur.com/a/3NkeN",
		"/hitb-slides":                          "https://www.dropbox.com/s/bzptq3bvbwr0vqf/HITB.pdf?dl=0",
		"/hack.lu":                              "https://speakerdeck.com/filosottile/the-heartbleed-test-adventure-at-hack-dot-lu-2014",
		"/fuzz-talk":                            "https://speakerdeck.com/filosottile/automated-testing-with-go-fuzz",
		"/entropy-talk":                         "https://speakerdeck.com/filosottile/the-plain-simple-reality-of-entropy",
		"/entropy-talk-ccc":                     "https://speakerdeck.com/filosottile/the-plain-simple-reality-of-entropy-at-32c3",
		"/Badfish/installer":                    "https://mega.co.nz/#!CQAW2SzA!oXMiMP1c4fLlNgBT8SzNINBMtxevEVTbIAklNeyd2Zg",
		"/newsletter":                           "https://words.filippo.io/dispatches/#/portal/signup",
		"/newsletter/manage":                    "https://words.filippo.io/dispatches/#/portal/account/newsletters",
		"/age-design":                           "https://docs.google.com/document/d/11yHom20CrsuX8KQJXBBw04s80Unjv8zCg_A7sPAX_9Y/preview",
		"/age/report":                           "https://github.com/FiloSottile/age/issues/new/choose",
		"/age/age.1":                            "https://htmlpreview.github.io/?https://github.com/FiloSottile/age/blob/main/doc/age.1.html",
		"/age/age-keygen.1":                     "https://htmlpreview.github.io/?https://github.com/FiloSottile/age/blob/main/doc/age-keygen.1.html",
		"/CV/":                                  "https://blog.filippo.io/hi/",
		"/atom.xml":                             "https://blog.filippo.io/rss/",
		"/psa-enable-automatic-updates-please/": "https://blog.filippo.io/psa-enable-automatic-updates-please/",
		"/salt-and-pepper/":                     "https://blog.filippo.io/salt-and-pepper/",
		"/the-heartbleed-test-at-owasp-slash-nyu-poly/":         "https://blog.filippo.io/the-heartbleed-test-at-owasp-slash-nyu-poly/",
		"/on-keybase-dot-io-and-encrypted-private-key-sharing/": "https://blog.filippo.io/on-keybase-dot-io-and-encrypted-private-key-sharing/",
		"/native-scrolling-and-iterm2/":                         "https://blog.filippo.io/native-scrolling-and-iterm2/",
		"/my-remote-shell-session-setup/":                       "https://blog.filippo.io/my-remote-shell-session-setup/",
		"/why-go-is-elegant-and-makes-my-code-elegant/":         "https://blog.filippo.io/why-go-is-elegant-and-makes-my-code-elegant/",
		// TODO: make this blog post URL shorter
		"/how-the-new-gmail-image-proxy-works-and-what-does-this-mean-for-you/": "https://blog.filippo.io/how-the-new-gmail-image-proxy-works-and-what-this-means-for-you/",
		"/the-ecb-penguin/": "https://blog.filippo.io/the-ecb-penguin/",
	} {
		path, url := path, url // grrrrrr...
		mux.HandleFunc("filippo.io"+path, func(rw http.ResponseWriter, r *http.Request) {
			httpReqs.WithLabelValues("[redirect]").Inc()
			redirectReqs.WithLabelValues(path).Inc()
			http.Redirect(rw, r, url, http.StatusFound)
		})
	}
}

var redirectReqs = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "redirect_requests_total",
	Help: "Redirect requests processed, partitioned by path.",
}, []string{"path"})
var goGetReqs = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "goget_requests_total",
	Help: "go get requests processed, partitioned by name and go-get query parameter.",
}, []string{"name", "go_get"})
var proxyErrs = promauto.NewCounter(prometheus.CounterOpts{
	Name: "proxy_errors_total",
	Help: "Plausible proxy errors.",
})
