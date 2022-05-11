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
	"strings"

	"github.com/awslabs/yesiscan/interfaces"
	"github.com/awslabs/yesiscan/iterator"
	"github.com/awslabs/yesiscan/util/errwrap"
	"github.com/awslabs/yesiscan/util/safepath"
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

	// path component
	if strings.HasPrefix(obj.Input, "/") {
		// XXX: we could auto-detect the dir bit, and deal with rel paths too
		isDir := strings.HasSuffix(obj.Input, "/")
		info, err := os.Stat(obj.Input) // XXX: stat or Lstat?
		if err != nil {
			return nil, err
		}
		if isDir != info.IsDir() {
			return nil, fmt.Errorf("input path must end with a trailing slash if it's a dir")
		}
		path, err := safepath.ParseIntoPath(obj.Input, isDir)
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

	// NOTE: it's unlikely that the url.Parse method ever errors.
	u, err := url.Parse(obj.Input)
	if err != nil {
		return nil, errwrap.Wrapf(err, "could not parse URL")
	}
	s := u.String()

	// TODO: consider adding HttpScheme as well (with a flag)
	if u.Scheme == iterator.HttpScheme {
		return nil, fmt.Errorf("plain http is currently blocked")
	}

	// this is a bit of a heuristic, but we'll go with it for now
	// this is because we get https:// urls that are really github git URI's
	if u.Scheme == iterator.HttpsSchemeRaw && strings.HasSuffix(s, iterator.ZipExtension) {
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

	// TODO: for now, just assume it can only be a git iterator...
	iterator := &iterator.Git{
		Debug: obj.Debug,
		Logf: func(format string, v ...interface{}) {
			obj.Logf("iterator: "+format, v...)
		},
		Prefix:        obj.Prefix,
		URL:           s, // TODO: pass a *net.URL instead?
		TrimGitSuffix: true,

		Parser: obj, // store a handle to the originator
	}
	iterators = append(iterators, iterator)
	return iterators, nil
}
