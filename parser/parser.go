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

package parser

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/awslabs/yesiscan/interfaces"
	"github.com/awslabs/yesiscan/iterator"
	"github.com/awslabs/yesiscan/util/errwrap"
	"github.com/awslabs/yesiscan/util/safepath"
	"github.com/go-git/go-git/v5/plumbing"
)

// TrivialURIParser takes input as a single string. It expects either a URL or a
// Path component as the input.
type TrivialURIParser struct {
	Debug  bool
	Logf   func(format string, v ...interface{})
	Prefix safepath.AbsDir

	Input string
}

func (obj *TrivialURIParser) String() string {
	return fmt.Sprintf("trivialuriparser(%s)", obj.Input)
}

func (obj *TrivialURIParser) Parse() ([]interfaces.Iterator, error) {
	if obj.Input == "" {
		return nil, fmt.Errorf("empty input")
	}

	iterators := []interfaces.Iterator{}

	// NOTE: it's unlikely that the url.Parse method ever errors.
	u, err := url.Parse(obj.Input)
	if err != nil {
		return nil, errwrap.Wrapf(err, "could not parse URL")
	}
	s := u.String()

	if obj.Debug {
		obj.Logf("scheme: %s", u.Scheme)
		obj.Logf("host: %s", u.Host)
		obj.Logf("path: %s", u.Path)
	}

	// TODO: consider allowing HttpSchemeRaw as well (with a flag)
	if strings.ToLower(u.Scheme) == iterator.HttpSchemeRaw {
		return nil, fmt.Errorf("plain http is currently blocked, did you mean https?")
	}

	// this is a bit of a heuristic, but we'll go with it for now
	// this is because we get https:// urls that are really github git URI's
	isTar := strings.HasSuffix(strings.ToLower(s), iterator.TarExtension)
	if strings.ToLower(u.Scheme) == iterator.HttpsSchemeRaw && (isZip(s) || isGzip(s) || isTar || isBzip2(s)) {
		iterator := &iterator.Http{
			Debug: obj.Debug,
			Logf: func(format string, v ...interface{}) {
				obj.Logf("iterator: "+format, v...)
			},
			Prefix:    obj.Prefix,
			URL:       s,     // TODO: pass a *net.URL instead?
			AllowHttp: false, // allow non-https ?

			Parser: obj, // store a handle to the originator
		}
		iterators = append(iterators, iterator)
		return iterators, nil
	}

	if isGit(u) {
		// TODO: for now, just assume it can only be a git iterator...
		// Checking if commit hash exists at the end of the URL.
		// examples of URLs of different hosts containing commit hashes:
		// github: https://github.com/awslabs/yesiscan/commit/496d080bc7fe835511d7220f127e118d0881b792
		// webrtc: https://webrtc.googlesource.com/src.git/+/c276aee4eda7b1a466b139838f20e790bd746309
		// TODO: Might need to be generalized in the future as we add more URL patterns. 
		hash := ""
		index := strings.LastIndex(u.Path, "/")
		pathSuffix := u.Path[index+1:]
		if plumbing.IsHash(pathSuffix) {
			hash = pathSuffix
			// Here we are removing the parts of the URL which are there because
			// of a commit hash such that the repository can be cloned properly.
			u.Path = u.Path[:index]
			index := strings.LastIndex(u.Path, "/")
			u.Path = u.Path[:index]
			s = u.String()
		}
		iterator := &iterator.Git{
			Debug: obj.Debug,
			Logf: func(format string, v ...interface{}) {
				obj.Logf("iterator: "+format, v...)
			},
			Prefix:        obj.Prefix,
			URL:           s, // TODO: pass a *net.URL instead?
			TrimGitSuffix: true,
			Hash:          hash,
			Parser:        obj, // store a handle to the originator
		}
		iterators = append(iterators, iterator)
		return iterators, nil
	}

	// path component (absolute or relative, file or dir)
	if u.Scheme == "" {
		// XXX: we could auto-detect the dir bit
		isDir := strings.HasSuffix(obj.Input, "/")
		info, err := os.Stat(obj.Input) // XXX: stat or Lstat?
		if err != nil {
			return nil, err
		}
		if isDir != info.IsDir() {
			return nil, fmt.Errorf("input path must end with a trailing slash if it's a dir")
		}

		p, err := filepath.Abs(obj.Input)
		if err != nil {
			return nil, err
		}
		if isDir {
			p += "/" // filepath.Abs calls filepath.Clean which strips this
		}

		path, err := safepath.ParseIntoPath(p, isDir)
		if err != nil {
			return nil, err
		}
		iterator := &iterator.Fs{
			Debug: obj.Debug,
			Logf: func(format string, v ...interface{}) {
				obj.Logf("iterator: "+format, v...)
			},
			Prefix: obj.Prefix,
			Path:   path,

			Parser: obj, // store a handle to the originator
		}
		iterators = append(iterators, iterator)
		return iterators, nil
	}

	obj.Logf("i'm not sure how to parse this URI, please report this if you think I should be able to!")
	return nil, fmt.Errorf("i'm not sure how to parse this uri")
}

// isGit is a small helper to decide if we should run the git iterator or not.
// TODO: we should expand this function as it's a heuristic. maybe we can do
// better overall and not need a heuristic. time will tell...
func isGit(u *url.URL) bool {
	if strings.ToLower(u.Scheme) == iterator.GitSchemeRaw {
		return true
	}
	if strings.ToLower(u.Scheme) == iterator.HttpsSchemeRaw {
		hosts := []string{"github.com", "webrtc.googlesource.com"}
		urlHost := strings.ToLower(u.Host)
		for _, host := range hosts {
			if urlHost == host {
				return true
			}
		}
	}

	return false
}

// isZip is a helper method to determine whether a string has a Zip extension
// suffix.
func isZip(input string) bool {
	extensions := []string{iterator.ZipExtension, iterator.JarExtension, iterator.WhlExtension}
	for _, extension := range extensions {
		if strings.HasSuffix(strings.ToLower(input), extension) {
			return true
		}
	}
	return false
}

// isGzip is a helper method to determine whether a string has a Gzip extension
// suffix.
func isGzip(input string) bool {
	for _, extension := range iterator.GzipExtensions {
		if strings.HasSuffix(strings.ToLower(input), extension) {
			return true
		}
	}
	return false
}

// isBzip2 is a helper method to determine whether a string has a Bzip2
// extension suffix.
func isBzip2(input string) bool {
	for _, extension := range iterator.Bzip2Extensions {
		if strings.HasSuffix(strings.ToLower(input), extension) {
			return true
		}
	}
	return false
}
