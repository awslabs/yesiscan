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

// TODO: should this be a subpackage?
package backend

import (
	"bufio"
	"bytes"
	"context"
	"regexp"
	"sort"
	"strings"

	"github.com/awslabs/yesiscan/interfaces"
	"github.com/awslabs/yesiscan/util/errwrap"
	"github.com/awslabs/yesiscan/util/licenses"
)

const (
	// RegexpMaxBytesLine sets a larger maximum for file line scanning than
	// the default of bufio.MaxScanTokenSize which is sort of small.
	RegexpMaxBytesLine = 1024 * 1024 * 8 // 8 MiB
)

// RegexpCore is a simple backend that uses regular expressions to find certain
// license strings. You should probably not use this backend directly, but wrap
// it with one of the other ones like Regexp.
type RegexpCore struct {
	Debug bool
	Logf  func(format string, v ...interface{})

	// Rules is a list of regexp license rules.
	Rules []*RegexpLicenseRule

	// Origin is the license field origin which is used if a non-SPDX ID is
	// specified. You can use this blank if you want. These are commonly
	// expressed in "reverse-dns" notation to provide a unique identifier
	// when naming your license. Eg: "yesiscan.awslabs.github.com".
	Origin string

	// MultipleMatch is set to true if you want the same regexp to be
	// allowed to match more than once in the same file. This is useful if
	// you want to be able to pull out every range where the pattern is
	// seen, even if you will keep getting the same license answer. Most of
	// the time you probably want to leave this as false.
	MultipleMatch bool

	// compiledRegexps is compiled list of the above Rules field. This is
	// done for performance reasons.
	compiledRegexps []*regexp.Regexp
}

func (obj *RegexpCore) String() string {
	return "regexp"
}

func (obj *RegexpCore) Setup(ctx context.Context) error {
	for i, x := range obj.Rules {
		r, err := regexp.Compile(x.Pattern)
		if err != nil {
			return errwrap.Wrapf(err, "regexp compile failed at index: %d", i)
		}
		obj.compiledRegexps = append(obj.compiledRegexps, r)
	}

	return nil
}

func (obj *RegexpCore) ScanData(ctx context.Context, data []byte, info *interfaces.Info) (*interfaces.Result, error) {
	if info.FileInfo.IsDir() {
		return nil, nil // skip
	}
	if len(data) == 0 {
		return nil, nil // skip
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	licenseMap := make(map[string]struct{})

	reader := bytes.NewReader(data)
	scanner := bufio.NewScanner(reader)
	buf := []byte{}                         // create a buffer for very long lines
	scanner.Buffer(buf, RegexpMaxBytesLine) // set the max size of that buffer
	for scanner.Scan() {
		// In an effort to short-circuit things if needed, we run a
		// check ourselves and break out early if we see that we have
		// cancelled early.
		select {
		case <-ctx.Done():
			return nil, errwrap.Wrapf(ctx.Err(), "scanner ended early")
		default:
		}

		s := scanner.Text() // newlines will be stripped here
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}

		for i, r := range obj.compiledRegexps {
			loc := r.FindStringIndex(s) // (loc []int)
			if loc == nil {             // no match
				continue
			}
			if obj.Debug {
				obj.Logf("matched: %s", string(s[loc[0]:loc[1]]))
			}

			lid := obj.Rules[i].ID
			// TODO: replace this with a generic license parser and
			// alias matcher.
			split := strings.Split(lid, " AND ")
			for _, l := range split {
				l = strings.TrimSpace(l)
				licenseMap[l] = struct{}{}
			}
			if !obj.MultipleMatch {
				break // just break this inner loop
			}
		}
	}
	var skip error
	scannerErr := scanner.Err()
	if scannerErr == bufio.ErrTooLong {
		skip = scannerErr // add to ignored files...
		scannerErr = nil  // reset
	}

	ids := []string{}
	for id := range licenseMap {
		ids = append(ids, id)
	}
	sort.Strings(ids) // deterministic order

	licenseList := []*licenses.License{}

	for _, id := range ids {
		license := &licenses.License{
			SPDX: id,
			// TODO: populate other fields here?
		}

		// If we find an unknown SPDX ID, we don't want to error,
		// because that would allow someone to put junk in their code to
		// prevent us scanning it. Instead, create an invalid license
		// but return it anyways. If we ever want to check validity, we
		// know to expect failures. It *must* be valid because it's an
		// explicit SPDX scanner.
		if err := license.Validate(); err != nil {
			//return nil, err
			license = &licenses.License{
				//SPDX: "",
				Origin: obj.Origin,
				Custom: id,
				// TODO: populate other fields here (eg: found license text)
			}
		}

		licenseList = append(licenseList, license)
	}

	if len(licenseMap) == 0 && skip == nil {
		// NOTE: this is NOT the same as interfaces.ErrUnknownLicense
		// because in this scenario, we're comfortable (ish) the parser
		// is exhaustive at finding a license with this methodology.
		// We want to return nil, but we error only if Scanner.Err() did
		// and so normally this returns nil, nil.
		return nil, errwrap.Wrapf(scannerErr, "regexp scanner error")
	}

	result := &interfaces.Result{
		Licenses:   licenseList,
		Confidence: 1.0, // TODO: what should we put here?
		Skip:       skip,
	}

	// We perform the strange task of processing any partial results, and
	// returning some even if we errored, because the spdx code seems to
	// think this is better than no results. I'll do the same, but there is
	// no guarantee the calling iterator will use these. (Currently it does
	// not!)
	return result, errwrap.Wrapf(scannerErr, "regexp scanner error")
}

// RegexpLicenseRule represents the data required for a regexp license rule.
// Reminder, you can use backticks to quote golang strings, which is
// particularly helpful when entering regular expressions into structs.
type RegexpLicenseRule struct {
	// Pattern is the expression we want to match. This uses the stock
	// golang regexp engine.
	Pattern string `json:"pattern"`

	// ID is the license ID we should use when the above pattern matches. It
	// should be an SPDX ID, but other strings are supported, they just
	// won't be treated as SPDX if they aren't in our database of allowed
	// license identifiers.
	ID string `json:"id"`

	// TODO: add a comment field?
	//Comment string `json:"comment"`
}
