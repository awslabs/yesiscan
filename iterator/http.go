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
//
// SPDX-License-Identifier: Apache-2.0

package iterator

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/awslabs/yesiscan/interfaces"
	"github.com/awslabs/yesiscan/util/errwrap"
	"github.com/awslabs/yesiscan/util/safepath"
)

const (
	// HttpScheme is the standard prefix used for http URL's.
	HttpScheme = "http://"

	// HttpsScheme is the standard prefix used for https URL's.
	HttpsScheme = "https://"

	// HttpSchemeRaw is the standard prefix used for http URL's but without
	// the scheme protocol separator which is <colon-slash-slash>.
	HttpSchemeRaw = "http"

	// HttpsSchemeRaw is the standard prefix used for https URL's but
	// without the scheme protocol separator which is <colon-slash-slash>.
	HttpsSchemeRaw = "https"

	// UnknownFileName is the filename used when the URL doesn't have an
	// obvious filename at the end that we can use.
	// TODO: is there a better name we can use? This is mostly arbitrary.
	UnknownFileName = ".unknown"
)

var (
	httpMapMutex *sync.Mutex
	httpMutexes  map[string]*sync.Mutex
)

func init() {
	httpMapMutex = &sync.Mutex{}
	httpMutexes = make(map[string]*sync.Mutex)
}

// Http is an iterator that takes an http URL to download and performs the
// download operation. It will eventually return an Fs iterator since there's no
// need for it to know how to walk through a filesystem tree itself. It can use
// a local cache so that future calls to the same URL won't have to waste
// bandwidth or cycles again but only in cases when we can determine it will be
// the same file. Please note this is named http, but we obviously support https
// as the most common form of this.
type Http struct {
	Debug  bool
	Logf   func(format string, v ...interface{})
	Prefix safepath.AbsDir

	// Parser is a pointer to the parser that returned this. If it wasn't
	// returned by a parser, leave this nil. If this iterator came from an
	// iterator, then the Iterator handle should be filled instead.
	Parser interfaces.Parser

	// Iterator is a pointer to the iterator that returned this. If it
	// wasn't returned by an iterator, leave this nil. If this iterator came
	// from a parser, then the Parser handle should be filled instead.
	Iterator interfaces.Iterator

	// URL is the http URL of the file that we want to download.
	// TODO: consider doing some clever parsing of well-known paths like
	// github-style URL's or internal company code repository URL's.
	URL string

	// AllowHttp specifies whether we're allowed to download http
	// (unencrypted) URLs.
	AllowHttp bool

	// iterators store the list of which iterators we created, so we know
	// which ones we have to close!
	iterators []interfaces.Iterator

	// unlock is a function that should be called as part of the Close
	// method once this resource is finished. It can be defined when
	// building this iterator in case we want a mechanism for the caller of
	// this iterator to tell the child when to unlock any in-use resources.
	// It must be safe to call this function more than once if necessary.
	// This is currently used privately.
	unlock func()
}

// String returns a human-readable representation of the http URL we're looking
// at. The output of this format is not guaranteed to be constant, so don't try
// to parse it.
func (obj *Http) String() string {
	return fmt.Sprintf("http: %s", obj.URL)
}

// Validate runs some checks to ensure this iterator was built correctly.
func (obj *Http) Validate() error {
	if obj.Logf == nil {
		return fmt.Errorf("the Logf function must be specified")
	}
	if err := obj.Prefix.Validate(); err != nil {
		return err
	}

	if obj.URL == "" {
		return fmt.Errorf("must specify a URL")
	}

	if _, err := url.Parse(obj.URL); err != nil {
		return err // not that url.Parse ever really errors :/
	}

	isHttp := strings.HasPrefix(strings.ToLower(obj.URL), HttpScheme)
	isHttps := strings.HasPrefix(strings.ToLower(obj.URL), HttpsScheme)
	if !isHttp && !isHttps {
		return fmt.Errorf("invalid scheme")
	}

	if isHttp && !obj.AllowHttp {
		// did you mean https ?
		return fmt.Errorf("the http scheme is not allowed without the allow http option")
	}

	return nil
}

// GetParser returns a handle to the parent parser that built this iterator if
// there is one.
func (obj *Http) GetParser() interfaces.Parser { return obj.Parser }

// GetIterator returns a handle to the parent iterator that built this iterator
// if there is one.
func (obj *Http) GetIterator() interfaces.Iterator { return obj.Iterator }

// Recurse runs a simple iterator that is responsible for downloading an http
// url into a local filesystem path. If this happens successfully, it
// will return a new FsIterator that is initialized to this root path.
func (obj *Http) Recurse(ctx context.Context, scan interfaces.ScanFunc) ([]interfaces.Iterator, error) {
	relDir := safepath.UnsafeParseIntoRelDir("http/")
	prefix := safepath.JoinToAbsDir(obj.Prefix, relDir)
	if err := os.MkdirAll(prefix.Path(), interfaces.Umask); err != nil {
		return nil, err
	}

	// make a unique ID for the directory
	// XXX: we can consider different algorithms or methods here later...
	now := strconv.FormatInt(time.Now().UnixMilli(), 10) // itoa but int64
	sum := sha256.Sum256([]byte(obj.URL + now))
	hashRelDir, err := safepath.ParseIntoRelDir(fmt.Sprintf("%x", sum))
	if err != nil {
		return nil, err
	}
	httpAbsDir := safepath.JoinToAbsDir(prefix, hashRelDir)

	httpMapMutex.Lock()
	mu, exists := httpMutexes[obj.URL]
	if !exists {
		mu = &sync.Mutex{}
		httpMutexes[obj.URL] = mu
	}
	httpMapMutex.Unlock()

	if obj.Debug {
		obj.Logf("locking: %s", obj.String())
	}
	mu.Lock() // locking happens here (unlock on all errors/returns!)
	once := &sync.Once{}
	obj.unlock = func() {
		fn := func() {
			if obj.Debug {
				obj.Logf("unlocking: %s", obj.String())
			}
			mu.Unlock()
		}
		once.Do(fn)
	}

	// XXX: unlock when context closes?

	u, err := url.Parse(obj.URL)
	if err != nil {
		// programming error
		obj.unlock()
		return nil, errwrap.Wrapf(err, "error parsing URL %s", obj.URL)
	}
	segments := strings.Split(u.Path, "/")
	fileName := UnknownFileName // default
	if len(segments) > 0 {
		fileName = segments[len(segments)-1]
	}

	relFile, err := safepath.ParseIntoRelFile(fileName)
	if err != nil {
		// programming error
		obj.unlock()
		return nil, err
	}

	//directory := httpAbsDir.Path()
	fullFileNameAbsFile := safepath.JoinToAbsFile(httpAbsDir, relFile)
	fullFileName := fullFileNameAbsFile.Path()

	// make the dir we put the downloaded file into
	if err := os.MkdirAll(httpAbsDir.Path(), interfaces.Umask); err != nil {
		obj.unlock()
		return nil, err
	}

	// This is one reason why we have a mutex.
	if _, err := os.Stat(fullFileName); err == nil {
		obj.Logf("file %s already exists, overwriting", obj.String())
	}

	// create blank file
	file, err := os.Create(fullFileName)
	if err != nil {
		obj.unlock()
		return nil, errwrap.Wrapf(err, "error writing file %s", fullFileNameAbsFile)
	}
	defer file.Close()

	obj.Logf("downloading %s into %s as %s", obj.URL, httpAbsDir, fileName)

	req, err := http.NewRequestWithContext(ctx, "GET", obj.URL, nil) // XXX: nil?
	if err != nil {
		obj.unlock()
		return nil, errwrap.Wrapf(err, "error building request for %s", obj.URL)
	}

	//tr := &http.Transport{
	//	IdleConnTimeout: 30 * time.Second,
	//}
	client := &http.Client{
		//Transport: tr,

		// If CheckRedirect is nil, the Client uses its default policy,
		// which is to stop after 10 consecutive requests.
		// CheckRedirect func(req *Request, via []*Request) error
		CheckRedirect: nil,
	}

	// TODO: add a recurring progress logf if it takes longer than 30 sec
	resp, err := client.Do(req)
	if err != nil {
		obj.unlock()
		return nil, errwrap.Wrapf(err, "error do-ing request for %s", obj.URL)
	}
	defer resp.Body.Close()

	// TODO: should we allow others?
	if resp.StatusCode != 200 {
		obj.unlock()
		return nil, fmt.Errorf("bad status code of: %d", resp.StatusCode)
	}

	// FIXME: add a variant that can take a context
	size, err := io.Copy(file, resp.Body)
	if err != nil {
		obj.unlock()
		return nil, errwrap.Wrapf(err, "error writing our file to disk at %s", fullFileNameAbsFile)
	}
	obj.Logf("copied: %d bytes to disk at %s", size, fullFileNameAbsFile)

	obj.iterators = []interfaces.Iterator{}

	if strings.HasPrefix(obj.URL, HttpScheme) {
		u.Scheme = HttpSchemeRaw
	}
	if strings.HasPrefix(obj.URL, HttpsScheme) {
		u.Scheme = HttpsSchemeRaw
	}
	u.Opaque = ""                         // encoded opaque data
	if _, has := u.User.Password(); has { // redact password
		u.User = url.UserPassword(u.User.Username(), "")
	}
	//u.Host = ? // host or host:port

	u.RawPath = ""       // encoded path hint (see EscapedPath method)
	u.ForceQuery = false // append a query ('?') even if RawQuery is empty
	v := url.Values{}
	v.Set("now", now)
	u.RawQuery = v.Encode() // encoded query values, without '?'
	u.Fragment = ""         // fragment for references, without '#'
	u.RawFragment = ""      // encoded fragment hint (see EscapedFragment method)

	// XXX: if it's a single zip file do we return a zip iterator here or do
	// we let the fs iterator sort that out...
	iterator := &Fs{
		Debug: obj.Debug,
		Logf: func(format string, v ...interface{}) {
			obj.Logf(format, v...) // TODO: add a prefix?
		},
		Prefix: obj.Prefix,

		Iterator: obj,

		// XXX: what path?
		Path: httpAbsDir,

		GenUID: func(safePath safepath.Path) (string, error) {
			if !safepath.HasPrefix(safePath, httpAbsDir) {
				// programming error
				return "", fmt.Errorf("path doesn't have prefix")
			}

			p := ""
			// remove httpAbsDir prefix from safePath to get a relPath
			relPath, err := safepath.StripPrefix(safePath, httpAbsDir)
			if err == nil {
				p = relPath.String()
			} else if err != nil && safePath.String() != httpAbsDir.String() {
				// programming error
				return "", errwrap.Wrapf(err, "problem stripping prefix")
			}

			x := *u // copy
			x.Path += "/" + p

			return x.String(), nil
		},

		//Unlock: unlock,
	}
	obj.iterators = append(obj.iterators, iterator)

	return obj.iterators, nil
}

// Close shuts down the iterator and/or performs clean up after the Recurse
// method has run. This must be called if you run Recurse.
func (obj *Http) Close() error {
	if obj.unlock != nil {
		obj.unlock()
	}
	var errs error
	for i := len(obj.iterators) - 1; i >= 0; i-- { // reverse order (stacks!)
		if err := obj.iterators[i].Close(); err != nil {
			errs = errwrap.Append(errs, err)
		}
	}
	return errs
}
