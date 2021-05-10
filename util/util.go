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

package util

import (
	"fmt"
	"net/url"
	"strings"
)

// ShellHyperlinkEncode takes a string, and a uri and returns a shell encoded
// representation of a hyperlink using the modern shell escaping sequence. Idea
// from: https://purpleidea.com/blog/2018/06/29/hyperlinks-in-gnome-terminal/
func ShellHyperlinkEncode(display string, uri string) string {
	x := uri // XXX: how do we escape correctly?
	//x := url.QueryEscape(uri) // XXX: this is the wrong escaping

	return "\033]8;;" + x + "\a" + display + "\033]8;;\a"
}

// SmartURI returns a "smart" URI given an internal UID that we have. The UID is
// the special string that's the unique identifier that's returned from each
// backend. We convert this into a "better" URI if we can. If we can't, we just
// return the uid unchanged.
// TODO: the different helper functions that are called within could be provided
// by each backend, instead of us writing them here and assuming how they work.
func SmartURI(uid string) string {
	// is this a github URI?
	if s, err := smartGithubURI(uid); err == nil {
		return s
	}

	return uid
}

// smartGithubURI attempts to return a useful URI from an internal Github UID.
// If we don't detect this as a github UID, then we error.
func smartGithubURI(uid string) (string, error) {
	u, err := url.Parse(uid)
	if err != nil {
		return "", err
	}

	if u.Scheme != "git" && u.Scheme != "https" {
		return "", fmt.Errorf("invalid scheme")
	}
	u.Scheme = "https" // make it user clickable

	if u.Host != "github.com" {
		return "", fmt.Errorf("wrong hostname")
	}

	q := u.Query()
	sha1s := q["sha1"]
	if len(sha1s) != 1 {
		return "", fmt.Errorf("wrong length of sha1s")
	}
	sha1 := sha1s[0]
	if sha1 == "" {
		return "", fmt.Errorf("unknown sha1")
	}
	u.RawQuery = "" // erase it

	p := strings.TrimPrefix(u.Path, "/")
	ps := strings.Split(p, "/")
	if len(ps) < 2 {
		return "", fmt.Errorf("invalid path")
	}

	u.Path = ps[0] + "/" + ps[1] + "/blob/" + sha1 + "/" + strings.Join(ps[2:], "/")

	u.RawPath = ""       // encoded path hint (see EscapedPath method)
	u.ForceQuery = false // append a query ('?') even if RawQuery is empty

	// TODO: add support for line number ranges, eg: #L13-L42 or just #L42

	u.Fragment = ""    // fragment for references, without '#'
	u.RawFragment = "" // encoded fragment hint (see EscapedFragment method)

	return u.String(), nil
}
