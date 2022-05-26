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

package webserver

import (
	"context"
	"embed"
	"encoding/base64"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"strings"

	"github.com/awslabs/yesiscan/art"
	"github.com/awslabs/yesiscan/iterator"
	"github.com/awslabs/yesiscan/lib"

	"github.com/gin-gonic/gin"
)

//go:embed static/*
var staticFS embed.FS

var base64Yesiscan string

func init() {
	// encode once at startup
	base64Yesiscan = base64.StdEncoding.EncodeToString(art.YesiscanSvg)
}

// Server is our webserver struct.
type Server struct {
	Program string
	Debug   bool
	Logf    func(format string, v ...interface{})
}

func (obj *Server) Run(ctx context.Context) error {
	addr := ":8000" // XXX: address
	//readTimeout := 60*60
	//writeTimeout := 60*60

	//conn, err := net.Listen("tcp", addr) // XXX: address?
	//if err != nil {
	//	return err
	//}
	//defer conn.Close()

	//serveMux := http.NewServeMux()

	//server := &http.Server{
	//	Addr: addr,
	//	Handler: serveMux,
	//	ReadTimeout: time.Duration(readTimeout) * time.Second,
	//	WriteTimeout: time.Duration(writeTimeout) * time.Second,
	//}

	//if err := server.Serve(conn); err != nil {
	//	return err
	//}
	router := obj.Router()

	router.Run(addr)

	return nil
}

//func (obj *Server) ServeHTTP(w http.ResponseWriter, req *http.Request) {
func (obj *Server) Router() *gin.Engine {

	router := gin.Default()

	logWriter := &LogWriter{
		Logf: obj.Logf,
	}
	router.Use(gin.LoggerWithWriter(logWriter))

	// icon by icons8: https://img.icons8.com/stickers/100/000000/search.svg

	var html = template.Must(template.New("index").Parse(`
<html>
<head>
<title>{{ .program }}</title>
<style>
input[type=text]:focus {
	background-color: lightblue;
}

input[type=text] {
	width: 80%;
	align: center;
	box-sizing: border-box;
	border: 2px solid #ccc;
	border-radius: 4px;
	font-size: 16px;
	background-image: url('/static/icons8-search.svg');
	background-size: 25px 25px;
	background-position: 10px 10px; 
	background-repeat: no-repeat;
	padding: 12px 20px 12px 40px;
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

div {
	text-align: center;
}

</style>
</head>
<body>
<div>

<h1 style="color:blue; text-align: center;">welcome to <a href="https://github.com/awslabs/yesiscan/"><img alt="yesiscan logo" height="100px" style="vertical-align: middle;" src="data:image/svg+xml;base64,{{ .image }}" /></a></h1>
<form action="/scan/" method="POST">
<input type="text" name="uri" placeholder="enter any uri"></input>
<!--<input class="submit" type="submit" value="submit">-->
</form>

<br />
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
</div>
</body>
</html>
`))

	router.SetHTMLTemplate(html)

	//	router.GET("/static/*filepath", func(c *gin.Context) {
	//		c.FileFromFS(path.Join("/web/", c.Request.URL.Path), http.FS(staticFS))
	//	})

	router.StaticFS("/static", mustFS()) // our files from embed

	router.GET("/index.html", func(c *gin.Context) {

		fmt.Printf("XXX: GET!!!...\n")

		//c.HTML(http.StatusOK, ...)
		//		c.JSON(http.StatusOK, gin.H{
		//			"program": obj.Program,
		//			"message": "hello!",
		//		})

		c.HTML(http.StatusOK, "index", gin.H{
			"program": obj.Program,
			"image":   base64Yesiscan,
			"status":  "success",
		})

	})

	scan := func(c *gin.Context) error {

		uri := c.PostForm("uri")
		uri = strings.TrimSpace(uri)
		if uri == "" {
			return fmt.Errorf("empty request")
		}

		obj.Logf("scan: %s", uri)

		// make sure we're only scanning public URI's, not local data!
		isGit := strings.HasPrefix(strings.ToLower(uri), iterator.GitScheme)
		isHttps := strings.HasPrefix(strings.ToLower(uri), iterator.HttpsScheme)
		// TODO: do we want to allow local use?
		if !isGit && !isHttps {
			return fmt.Errorf("must pass in git or https uri's")
		}
		// TODO: what other sort of uri sanitation do we need to do?

		args := []string{uri}

		flags := make(map[string]bool)
		names := []string{
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
		for _, f := range names {
			val, exists := c.GetPostForm("names")
			if exists {
				flags[f] = false
				if val == "true" || val == "TRUE" { // TODO add others?
					flags[f] = true
				}
			}
		}

		// XXX: run in a goroutine (and queue up the jobs...)
		// XXX: return output...
		// XXX: handle cancellation for server shutdown...
		m := &lib.Main{
			Program: obj.Program,
			Debug:   obj.Debug,
			Logf:    obj.Logf,

			Args:  args,
			Flags: flags,

			//Profiles: []string{}, // XXX: add these

			//RegexpPath: "", // XXX: add me?
		}
		err := m.Run(context.TODO())

		// XXX

		return err
	}

	router.POST("/scan/", func(c *gin.Context) {
		if err := scan(c); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"message": err.Error(),
			})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"message": "XXX???",
		})
	})

	//router.ServeHTTP(w, req) // pass through

	return router
}

// mustFS is a helper function so we can return static files that we added with
// the embed package.
func mustFS() http.FileSystem {
	sub, err := fs.Sub(staticFS, "static")
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
