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

package ansi

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"golang.org/x/term"
)

// Logf is a complex printing thing to do some ansi terminal escape sequence
// magic.
// FIXME: there might be bugs if Ellipsis is very big and Width is very small.
type Logf struct {
	// Prefix is a prefix to append to each message. You can leave this
	// empty.
	Prefix string

	// Ellipsis is what is appended to the end of each message when
	// truncating. You can leave this empty.
	Ellipsis string

	// Enable specifies whether you want to turn this on or not.
	Enable bool

	// Prefixes are a list of string prefixes to match when deciding to
	// delete a previous entry.
	Prefixes []string

	mutex      *sync.Mutex
	previous   string
	isTerminal bool
	width      int
}

// Init must be called once before Logf is used. As a convenience, this returns
// the Logf function that you should use!
func (obj *Logf) Init() func(format string, v ...interface{}) {
	obj.mutex = &sync.Mutex{}
	//obj.previous = ""
	obj.isTerminal = term.IsTerminal(0)
	var err error
	obj.width, _, err = term.GetSize(0)
	if err != nil {
		obj.isTerminal = false // keep it simple, who cares
	}

	return obj.Logf
}

// Logf is the actual Logf function you should use. You must run Init before
// you use this.
func (obj *Logf) Logf(format string, v ...interface{}) {
	s := fmt.Sprintf(format, v...)

	if obj.isTerminal {
		// TODO: what about multi-char width UTF-8 stuff?
		if len(s) > obj.width-len(obj.Prefix) { // truncate/ellipsize
			s = s[0:obj.width-len(obj.Prefix)-len(obj.Ellipsis)] + obj.Ellipsis
		}
	}
	s = s + "\n" // add the newline in

	obj.mutex.Lock() // for safety
	validPrefix := false
	for _, p := range obj.Prefixes {
		b := strings.HasPrefix(obj.previous, p)
		validPrefix = validPrefix || b
	}

	if obj.Enable && obj.previous != "" && validPrefix {
		// move up 1 line, clear to left
		fmt.Fprint(os.Stderr, "\033[1A\033[K") // not 1K as you'd think
	}
	fmt.Fprint(os.Stderr, obj.Prefix+s) // actually print

	obj.previous = s // save for later
	obj.mutex.Unlock()
}
