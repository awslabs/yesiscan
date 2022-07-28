// Copyright Amazon.com Inc or its affiliates and the project contributors
// Written by James Shubin <purple@amazon.com> and the project contributors
//
// Licensed under the Apache License, Version 2.0 (the "License"); you may not
// use this file except in compliance with the License. You may obtain a copy of
// the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
// WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the
// License for the specific language governing permissions and limitations under
// the License.
//
// We will never require a CLA to submit a patch. All contributions follow the
// `inbound == outbound` rule.
//
// This is not an official Amazon product. Amazon does not offer support for
// this project.

package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/awslabs/yesiscan/art"
	"github.com/awslabs/yesiscan/interfaces"
	"github.com/awslabs/yesiscan/iterator"
	"github.com/awslabs/yesiscan/lib"
	"github.com/awslabs/yesiscan/util"
	"github.com/awslabs/yesiscan/util/errwrap"
	"github.com/awslabs/yesiscan/util/safepath"

	"github.com/gin-contrib/multitemplate"
	"github.com/gin-gonic/gin"
)

const (
	// YesiscanCookieNameFlags is the name of the cookie used to store flags
	// settings.
	YesiscanCookieNameFlags = "yesiscan_flags"

	// YesiscanCookieNameProfiles is the name of the cookie used to store
	// profiles settings.
	YesiscanCookieNameProfiles = "yesiscan_profiles"

	fancyRendering = true

	displaySummary = true

	serverAddr = ":8000"
)

//go:embed static/*
var staticFs embed.FS

var base64Yesiscan string

// XXX: get this list from some globals?
var flagNames = []string{
	"no-backend-licenseclassifier",
	"no-backend-spdx",
	"no-backend-askalono",
	"no-backend-scancode",
	"no-backend-bitbake",
	"no-backend-regexp",
	"yes-backend-licenseclassifier",
	"yes-backend-spdx",
	"yes-backend-askalono",
	"yes-backend-scancode",
	"yes-backend-bitbake",
	"yes-backend-regexp",
}

func init() {
	// encode once at startup
	base64Yesiscan = base64.StdEncoding.EncodeToString(art.YesiscanSvg)
}

// Server is our web server struct.
type Server struct {
	Program string
	Debug   bool
	Logf    func(format string, v ...interface{})

	// Profiles is the list of profiles to allow. Either the names from
	// ~/.config/yesiscan/profiles/<name>.json or full paths.
	Profiles []string

	// reportPrefix is the path where we store and load the reports from.
	reportPrefix safepath.AbsDir
}

func (obj *Server) Run(ctx context.Context) error {
	obj.Logf("server: startup...")

	userCacheDir, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(userCacheDir, interfaces.Umask); err != nil {
		return err
	}
	prefix := filepath.Join(userCacheDir, obj.Program)
	if err := os.MkdirAll(prefix, interfaces.Umask); err != nil {
		return err
	}
	safePrefixAbsDir, err := safepath.ParseIntoAbsDir(prefix)
	if err != nil {
		return err
	}
	//obj.Logf("prefix: %s", safePrefixAbsDir)

	//home, err := os.UserHomeDir()
	//if err != nil {
	//	obj.Logf("error finding home directory: %+v", err)
	//}

	relDir := safepath.UnsafeParseIntoRelDir("report/")
	obj.reportPrefix = safepath.JoinToAbsDir(safePrefixAbsDir, relDir)
	if err := os.MkdirAll(obj.reportPrefix.Path(), interfaces.Umask); err != nil {
		return err
	}
	obj.Logf("report prefix: %s", obj.reportPrefix)

	//readTimeout := 60*60
	//writeTimeout := 60*60
	//conn, err := net.Listen("tcp", serverAddr)
	//if err != nil {
	//	return err
	//}
	//defer conn.Close()
	//serveMux := http.NewServeMux()
	//server := &http.Server{
	//	Addr: serverAddr,
	//	Handler: serveMux,
	//	ReadTimeout: time.Duration(readTimeout) * time.Second,
	//	WriteTimeout: time.Duration(writeTimeout) * time.Second,
	//}
	//if err := server.Serve(conn); err != nil {
	//	return err
	//}
	router := obj.Router()

	router.Run(serverAddr)

	return nil
}

//func (obj *Server) ServeHTTP(w http.ResponseWriter, req *http.Request) {
func (obj *Server) Router() *gin.Engine {
	if !obj.Debug {
		gin.SetMode(gin.ReleaseMode)
	}

	router := gin.Default()

	logWriter := &LogWriter{
		Logf: obj.Logf,
	}
	router.Use(gin.LoggerWithWriter(logWriter))

	// icon by icons8: https://img.icons8.com/stickers/100/000000/search.svg
	// icon by icons8: https://img.icons8.com/stickers/100/000000/checkmark.svg

	var index = `
<html>
<head>
<title>{{ .program }}</title>
<style>
input[type=text]:focus {
	background-color: lightblue;
}

input[type=text] {
	width: 80%;
	box-sizing: border-box;
	border: 2px solid #ccc;
	border-radius: 4px;
	font-size: 16px;
	background-image: url('/static/icons8-search.svg');
	background-size: 25px 25px;
	background-position: left 10px top 10px;
	background-repeat: no-repeat;
	/* https://css-tricks.com/almanac/properties/b/background-position/
	background-image: url('/static/icons8-search.svg'), url('/static/icons8-checkmark.svg');
	background-size: 25px 25px, 25px 25px;
	background-position: left 10px top 10px, right 10px top 10px;
	background-repeat: no-repeat, no-repeat;
	*/
	padding: 12px 20px 12px 40px;
}

input[type=image] {
	width: 40px;
	height: 40px;
}

.submit {
	background-color: white;
	color: black;
	box-sizing: border-box;
	border: 2px solid #ccc;
	border-radius: 4px;
	font-size: 16px;
}

.submit:hover {
	background-color: #008CBA;
	color: white;
}

#flags {
	border-collapse: collapse;
	width: 80%;
	margin-left: auto;
	margin-right: auto;
	font-size: 8px;
}

#plainprofiles {
	border-collapse: collapse;
	width: 80%;
	margin-left: auto;
	margin-right: auto;
	font-size: 8px;
}

#flagstable,#profilestable {
	width: 80%;
	margin-left: auto;
	margin-right: auto;
	font-size: 16px;
}

#profiles {
	box-sizing: border-box;
	overflow: hidden;
	padding: 5px;
	width: 80%;
	margin-left: auto;
	margin-right: auto;
}

/* Hide scrollbar for Chrome, Safari and Opera */
.scrollbar-hidden::-webkit-scrollbar {
	display: none;
}

/* Hide scrollbar for IE, Edge and Firefox */
.scrollbar-hidden {
	-ms-overflow-style: none;
	scrollbar-width: none; /* Firefox */
}

select {
	box-sizing: border-box;
	padding: 5px;
	border: none;
	width: 100%;
}

option {
	box-sizing: border-box;
	text-align: center;
	border: 1px solid #000;
	background-color: white;
	display: inline-block;
	float: left;
	padding: 10px;
	margin-right: 5px;
	margin-left: 5px;
}

/* selection hack from:
https://stackoverflow.com/questions/35981567/preventing-change-in-colour-and-background-colour-of-selected-option-when-blurre/35982030
*/
option:checked {
	color: white;
	-webkit-text-fill-color: white;
	background: #4a90d9 repeat url("/static/4a90d9.jpg");
}

#error {
	border-collapse: collapse;
	width: 80%;
	margin-left: auto;
	margin-right: auto;
	background-color: #ff0000;
}

#error td, #error th {
	border: 1px solid #ddd;
	padding: 8px;
}

#report {
	border-collapse: collapse;
	width: 80%;
	margin-left: auto;
	margin-right: auto;
}

#report td, #report th {
	border: 1px solid #ddd;
	padding: 8px;
}

#report tr:nth-child(even){background-color: #f2f2f2;}

#report tr:hover {background-color: #ddd;}

#report th {
	padding-top: 12px;
	padding-bottom: 12px;
	text-align: left;
	background-color: #042ea9;
	color: white;
}

#summary {
	background-color: #ffffff;
}

#summary th {
	padding-top: 6px;
	padding-bottom: 6px;
	text-align: left;
	background-color: #4a90d9;
	color: white;
}

#summary tr:nth-child(even){background-color: unset;}

#summary tr:hover {background-color: unset;}


</style>
</head>
<body>
<div style="text-align: center;">

<h1 style="color:#042ea9; text-align: center;">welcome to <a href="/"><img alt="yesiscan logo" height="100px" style="vertical-align: middle;" src="data:image/svg+xml;base64,{{ .image }}" /></a></h1>
<form action="/scan/" method="POST">
<div id="forminput" style="text-align: center;">
	<input type="text" name="uri" placeholder="enter any uri" value="{{ .uri }}"></input>
	<!-- XXX: how do I add this submit button, but keep it all centred?
	<div>&nbsp;</div>
	<input type="image" src="/static/icons8-checkmark.svg"></input>
	-->
</div>

{{ $fkeys := sortedmapkeys .flags }}
{{ $flags := .flags }}

<table id="flagstable"><tr><td style="width: 0px;">backends:</td><td>
<table id="flags"><tr>
{{ $n := len $fkeys }}
{{ range $i, $v := $fkeys }}
	{{ if hasprefix . "yes-backend-" }}
		<td><input type="checkbox" id="{{ . }}" name="{{ . }}" value="true"{{ if ischecked $flags . }} checked{{ end }}/></td>
		<td><label for="{{ . }}">{{ trimprefix . "yes-backend-" }}</label></td>
		<!-- separator {{ if ne (plus1 $i) $n }}<td>|</td>{{ end }}-->
	{{ end }}
{{ end }}
</tr></table>
</table>

{{ if .fancy }}

{{ $pkeys := sortedmapkeys .profiles }}
{{ $profiles := .profiles }}

<table id="profilestable"><tr><td style="width: 0px;">profiles:</td><td>
<div id="profiles">
	<select multiple name="profile" size="1" class="scrollbar-hidden">
{{ $n := len $pkeys }}
{{ range $i, $v := $pkeys }}
	<option id="{{ . }}"{{ if ischecked $profiles . }} selected{{ end }}>{{ . }}</option>
{{ end }}
	</select>
</div>
</td></tr></table>

{{ else }}

{{ $pkeys := sortedmapkeys .profiles }}
{{ $profiles := .profiles }}

<table id="plainprofiles"><tr>
{{ $n := len $pkeys }}
{{ range $i, $v := $pkeys }}
	<td><input type="checkbox" id="{{ . }}" name="profile" value="{{ . }}"{{ if ischecked $profiles . }} checked{{ end }}/></td>
	<td><label for="{{ . }}">{{ . }}</label></td>
	<!-- separator {{ if ne (plus1 $i) $n }}<td>|</td>{{ end }}-->
{{ end }}
</tr></table>

{{ end }}

<!--<input class="submit" type="submit" value="submit">-->
</form>
</div>

{{ .body }}

<br />
<div style="text-align: center;">
<pre style="color:lightgrey;">
Copyright Amazon.com Inc or its affiliates and the project contributors
Written by James Shubin (purple@amazon.com) and the project contributors

Licensed under the Apache License, Version 2.0 (the "License"); you may not
use this file except in compliance with the License. You may obtain a copy of
the License at

http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the
License for the specific language governing permissions and limitations under
the License.

We will never require a CLA to submit a patch. All contributions follow the
'inbound == outbound' rule.

This is not an official Amazon product. Amazon does not offer support for
this project.
</pre>
<a href="https://github.com/awslabs/yesiscan/">https://github.com/awslabs/yesiscan/</a>
</div>
</body>
</html>
`

	//var foo = template.Must(template.New("foo").Parse(``)
	//router.SetHTMLTemplate(foo)

	funcMap := map[string]interface{}{
		"hello": func() (string, error) {
			return "@purpleidea says hi!", nil
		},
		"sortedmapkeys": func(m map[string]bool) ([]string, error) {
			l := []string{}
			for k := range m {
				l = append(l, k)
			}
			sort.Strings(l)

			return l, nil
		},
		"hasprefix": func(s, prefix string) (bool, error) {
			return strings.HasPrefix(s, prefix), nil
		},
		"trimprefix": func(s, prefix string) (string, error) {
			return strings.TrimPrefix(s, prefix), nil
		},
		"ischecked": func(flags map[string]bool, key string) (bool, error) {
			val, ok := flags[key]
			if !ok {
				return false, nil
			}
			return val, nil
		},
		"plus1": func(x int) int { // https://go.dev/play/p/V94BPN0uKD
			return x + 1
		},
	}
	r := multitemplate.NewRenderer()
	r.AddFromStringsFuncs("index", funcMap, index)
	//r.AddFromStringsFuncs("report", funcMap, report)
	router.HTMLRender = r

	//	router.GET("/static/*filepath", func(c *gin.Context) {
	//		c.FileFromFS(path.Join("/web/", c.Request.URL.Path), http.FS(staticFs))
	//	})

	router.StaticFS("/static", mustFs()) // our files from embed

	// TODO: should we do it like this or just have one index?
	router.GET("/", func(c *gin.Context) {
		c.Redirect(http.StatusFound, "/index.html")
	})

	router.GET("/index.html", func(c *gin.Context) {

		c.HTML(http.StatusOK, "index", gin.H{
			"program":  obj.Program,
			"image":    base64Yesiscan,
			"status":   "success",
			"flags":    obj.getCookieFlags(c),
			"profiles": obj.getCookieProfiles(c),
			"fancy":    fancyRendering,
		})
	})

	scan := func(c *gin.Context) (string, error) {

		uri := c.PostForm("uri")
		uri = strings.TrimSpace(uri)
		if uri == "" {
			return "", fmt.Errorf("empty request")
		}

		obj.Logf("scan: %s", uri)

		// make sure we're only scanning public URI's, not local data!
		isGit := strings.HasPrefix(strings.ToLower(uri), iterator.GitScheme)
		isHttps := strings.HasPrefix(strings.ToLower(uri), iterator.HttpsScheme)
		// TODO: do we want to allow local use?
		if !isGit && !isHttps {
			return "", fmt.Errorf("must pass in git or https uri's")
		}
		// TODO: what other sort of uri sanitation do we need to do?

		args := []string{uri}

		flags := make(map[string]bool)
		values := url.Values{}
		for _, f := range flagNames {
			flags[f] = false // default so it shows up physically
			val, exists := c.GetPostForm(f)
			if !exists {
				continue
			}
			if val == "true" || val == "TRUE" { // TODO add others?
				flags[f] = true
				values.Set(f, "true")
			}
		}
		// XXX: if no backends are chosen, warn user that all will run!

		// TODO: save list of flags to cookies only if "save settings" flag is set

		// only allow user to choose profile from available list
		pvalues := url.Values{}
		profiles := []string{}
		profilesMap := make(map[string]bool)
		profilesPost := c.PostFormArray("profile")
		for _, x := range obj.Profiles {
			profilesMap[x] = false // default so it shows up physically
			if util.StrInList(x, profilesPost) {
				profiles = append(profiles, x)
				profilesMap[x] = true
				pvalues.Set(x, "true")
			}
		}

		maxAge := 2147483647 // 2^31 - 1 = 2147483647 = 2038-01-19 04:14:07
		// SetCookie(name, value string, maxAge int, path, domain string, secure, httpOnly bool)
		//c.SetCookie("gin_cookie", values.Encode(), maxAge, "/", "localhost", false, true)
		http.SetCookie(c.Writer, &http.Cookie{
			Name:   YesiscanCookieNameFlags,
			Value:  url.QueryEscape(values.Encode()),
			MaxAge: maxAge,
			Path:   "/",
			//Domain: "localhost",
			//SameSite: http.SameSiteDefaultMode,
			Secure:   false,
			HttpOnly: true,
		})

		http.SetCookie(c.Writer, &http.Cookie{
			Name:   YesiscanCookieNameProfiles,
			Value:  url.QueryEscape(pvalues.Encode()),
			MaxAge: maxAge,
			Path:   "/",
			//Domain: "localhost",
			//SameSite: http.SameSiteDefaultMode,
			Secure:   false,
			HttpOnly: true,
		})

		// XXX: run in a goroutine (and queue up the jobs...)
		// XXX: handle cancellation for server shutdown...
		m := &lib.Main{
			Program: obj.Program,
			Debug:   obj.Debug,
			Logf:    obj.Logf,

			Args:  args,
			Flags: flags,

			Profiles: profiles,

			//RegexpPath: "", // XXX: add me?
		}
		output, err := m.Run(context.TODO())
		if err != nil {
			return "", err
		}

		s, err := ReturnOutputHtml(output)
		if err != nil {
			return "", err
		}

		report := &Report{
			Uri:      uri,
			Flags:    flags,
			Profiles: profilesMap,
			// XXX: consider storing full datastructure of profiles
			Html: s,
			// XXX: consider storing output instead of HTML
		}

		//store and get a URL...
		u, err := obj.Store(report)
		if err != nil {
			return "", err
		}

		return u, nil
	}

	// XXX: add to a queue and stick us on the processing page (report)
	router.POST("/scan/", func(c *gin.Context) {
		u, err := scan(c) // XXX: run in a goroutine and wait for result
		if err != nil {
			//c.JSON(http.StatusBadRequest, gin.H{
			//	"message": err.Error(),
			//})
			e := `<table id="error">`
			x := err.Error()
			e += fmt.Sprintf(`<tr><th style="text-align: center"><i>%s</i></th></tr>`, x)
			e += "</table>"

			c.HTML(http.StatusOK, "index", gin.H{
				"program":  obj.Program,
				"image":    base64Yesiscan,
				"status":   "success",
				"body":     template.HTML(e), // avoid escaping the html!
				"uri":      c.PostForm("uri"),
				"flags":    obj.getCookieFlags(c),
				"profiles": obj.getCookieProfiles(c),
				"fancy":    fancyRendering,
			})
			return
		}

		c.Redirect(http.StatusFound, fmt.Sprintf("/report/?r=%s", u))
	})

	router.GET("/report/", func(c *gin.Context) {
		r := c.Query("r") // shortcut for c.Request.URL.Query().Get("r")
		if r == "" {
			//c.JSON(http.StatusBadRequest, gin.H{
			//	"message": fmt.Errorf("empty request"),
			//})
			e := `<table id="error">`
			x := fmt.Errorf("empty request").Error()
			e += fmt.Sprintf(`<tr><th style="text-align: center"><i>%s</i></th></tr>`, x)
			e += "</table>"

			c.HTML(http.StatusOK, "index", gin.H{
				"program":  obj.Program,
				"image":    base64Yesiscan,
				"status":   "success",
				"body":     template.HTML(e), // avoid escaping the html!
				"uri":      c.PostForm("uri"),
				"flags":    obj.getCookieFlags(c),
				"profiles": obj.getCookieProfiles(c),
				"fancy":    fancyRendering,
			})
			return
		}
		obj.Logf("report: %s", r)

		// XXX: return a report in progress message if a job exists
		report, err := obj.Load(r)
		if err != nil {
			//c.JSON(http.StatusBadRequest, gin.H{
			//	"message": err.Error(),
			//})
			e := `<table id="error">`
			x := err.Error()
			e += fmt.Sprintf(`<tr><th style="text-align: center"><i>%s</i></th></tr>`, x)
			e += "</table>"

			c.HTML(http.StatusOK, "index", gin.H{
				"program":  obj.Program,
				"image":    base64Yesiscan,
				"status":   "success",
				"body":     template.HTML(e), // avoid escaping the html!
				"uri":      c.PostForm("uri"),
				"flags":    obj.getCookieFlags(c),
				"profiles": obj.getCookieProfiles(c),
				"fancy":    fancyRendering,
			})
			return
		}

		c.HTML(http.StatusOK, "index", gin.H{
			"program":  obj.Program,
			"image":    base64Yesiscan,
			"status":   "success",
			"body":     template.HTML(report.Html), // avoid escaping the html!
			"uri":      report.Uri,
			"flags":    report.Flags,
			"profiles": report.Profiles,
			"fancy":    fancyRendering,
		})
	})

	//router.ServeHTTP(w, req) // pass through

	return router
}

// TODO: consider adding a context.Context
func (obj *Server) Store(report *Report) (string, error) {
	if report == nil {
		return "", fmt.Errorf("got nil report")
	}
	// make a unique ID for the file
	// XXX: we can consider different algorithms or methods here later...
	now := strconv.FormatInt(time.Now().UnixMilli(), 10) // itoa but int64
	sum := sha256.Sum256([]byte(report.Html + now))      // XXX: for now
	uid := fmt.Sprintf("%x", sum)
	hashRelFile, err := safepath.ParseIntoRelFile(fmt.Sprintf("%s.json", uid))
	if err != nil {
		return "", err
	}
	// TODO: split into subfolders when we have very large numbers of files
	absFile := safepath.JoinToAbsFile(obj.reportPrefix, hashRelFile)
	obj.Logf("report: %s", absFile)

	b, err := json.Marshal(report)
	if err != nil {
		return "", err
	}

	if err := os.WriteFile(absFile.Path(), b, os.ModePerm); err != nil {
		return "", errwrap.Wrapf(err, "error writing our file to disk at %s", absFile)
	}

	return uid, nil
}

// TODO: consider adding a context.Context
// TODO: we have no auth on this at the moment, anyone can lookup a report
func (obj *Server) Load(uid string) (*Report, error) {
	if len(uid) != 64 { // length of a sha256sum
		return nil, fmt.Errorf("invalid uid length")
	}

	// remove all the valid characters, it should be empty!
	// NOTE: this importantly also blocks path traversal hacks like ../ too!
	if cut := strings.Trim(uid, "0123456789abcdef"); len(cut) != 0 {
		return nil, fmt.Errorf("invalid uid characters")
	}

	hashRelFile, err := safepath.ParseIntoRelFile(fmt.Sprintf("%s.json", uid))
	if err != nil {
		return nil, err
	}
	// TODO: lookup from subfolders when we have very large numbers of files
	absFile := safepath.JoinToAbsFile(obj.reportPrefix, hashRelFile)
	obj.Logf("report: %s", absFile)

	b, err := os.ReadFile(absFile.Path())
	if err != nil {
		return nil, errwrap.Wrapf(err, "error reading our file from disk at %s", absFile)
	}

	buf := bytes.NewBuffer(b)
	decoder := json.NewDecoder(buf)

	var report Report // this gets populated during decode
	if err := decoder.Decode(&report); err != nil {
		return nil, errwrap.Wrapf(err, "error decoding the json from disk at %s", absFile)
	}
	if &report == nil {
		return nil, fmt.Errorf("empty report")
	}

	return &report, nil
}

func (obj *Server) getCookieFlags(c *gin.Context) map[string]bool {
	// build the default set of flags to display on a new page
	flags := make(map[string]bool)
	for _, x := range flagNames {
		flags[x] = true // default all to true
	}

	// load list from cookies
	if cookie, err := c.Cookie(YesiscanCookieNameFlags); err == nil {
		m, err := url.ParseQuery(cookie) // map[string][]string
		if err == nil && cookie != "" {
			for _, x := range flagNames {
				flags[x] = false // default all to true
			}
			for name := range m {
				if _, exists := flags[name]; exists {
					for _, x := range m[name] {
						if x == "true" {
							flags[name] = true
						}
					}
				}
			}
		}
	}

	return flags
}

func (obj *Server) getCookieProfiles(c *gin.Context) map[string]bool {

	profiles := make(map[string]bool)
	for _, x := range obj.Profiles {
		profiles[x] = true // default all to true
	}

	if cookie, err := c.Cookie(YesiscanCookieNameProfiles); err == nil {
		m, err := url.ParseQuery(cookie) // map[string][]string
		if err == nil && cookie != "" {
			for _, x := range obj.Profiles {
				profiles[x] = false // default all to true
			}
			for name := range m {
				if _, exists := profiles[name]; exists {
					for _, x := range m[name] {
						if x == "true" {
							profiles[name] = true
						}
					}
				}
			}
		}
	}

	return profiles
}

// Report is the struct containing everything from scanning.
type Report struct {
	// Uri is the input URI used for the scan.
	Uri string `json:"uri"`

	// Flags are a map of specified options that users may specify.
	Flags map[string]bool `json:"flags"`

	// Profiles are a set of specified profile names that users may specify.
	Profiles map[string]bool `json:"profiles"`

	// Html is a rendered version of the core report content.
	// XXX: we might choose to store the data itself in the future...
	Html string `json:"html"`
}

// ReturnOutputHtml returns a string of output, formatted in html.
func ReturnOutputHtml(output *lib.Output) (string, error) {
	if len(output.Results) == 0 {
		// handle this here, otherwise we'll get an error below...
		s := `<table id="report">`
		x := "no results obtained"
		s += fmt.Sprintf(`<tr><th style="text-align: center"><i>%s</i></th></tr>`, x)
		s += "</table>"
		return s, nil
	}

	str := ""
	for _, x := range output.Profiles {
		pro, err := lib.SimpleProfiles(output.Results, output.ProfilesData[x], displaySummary, output.BackendWeights, "html")
		if err != nil {
			return "", err
		}
		s := `<table id="report">`
		s += fmt.Sprintf(`<tr><th style="text-align: left">profile <i>%s</i>:</th></tr>`, x)
		s += fmt.Sprintf("%s", pro)
		s += "</table>"
		str += s + "<br />"
	}

	return str, nil
}

// mustFs is a helper function so we can return static files that we added with
// the embed package.
func mustFs() http.FileSystem {
	sub, err := fs.Sub(staticFs, "static")
	if err != nil {
		panic(err)
	}
	return http.FS(sub)
}

type LogWriter struct {
	Logf func(format string, v ...interface{})
}

func (obj *LogWriter) Write(p []byte) (n int, err error) {
	obj.Logf(string(p))
	return len(p), nil
}
