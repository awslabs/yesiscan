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
	// SpdxMaxBytesLine sets a larger maximum for file line scanning than
	// the default of bufio.MaxScanTokenSize which is sort of small.
	SpdxMaxBytesLine = 1024 * 1024 * 8 // 8 MiB

	// magicStringSPDX is the string we look for when trying to find an ID.
	magicStringSPDX = "SPDX-License-Identifier:"

	// magicNumberSPDX is... a bad parser hack that SPDX recommends.
	magicNumberSPDX = 5
)

var (
	// stripTrashSPDX is taken from the spdx tools repository.
	stripTrashSPDX = regexp.MustCompile(`[^\w\s\d.\-\+()]+`)
)

// Spdx is based on the Software Package Data Exchange project. It is built
// with a slightly objectionable parser as prescribed in the official tools
// repo.
type Spdx struct {
	Debug bool
	Logf  func(format string, v ...interface{})
}

func (obj *Spdx) String() string {
	return "spdx"
}

func (obj *Spdx) ScanData(ctx context.Context, data []byte, info *interfaces.Info) (*interfaces.Result, error) {
	if info.FileInfo.IsDir() {
		return nil, nil // skip
	}
	if len(data) == 0 {
		return nil, nil // skip
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	licenseMap := make(map[string]struct{})

	// An official parser for SPDX ID's seems be:
	// https://github.com/spdx/tools-golang/blob/a16d50ee155238df280a68252acc25e9afb7acea/idsearcher/idsearcher.go#L269
	// If it's meant to be that simplistic, we'll implement something
	// similar. Please report bugs over there before you report them here =D

	reader := bytes.NewReader(data)
	scanner := bufio.NewScanner(reader)
	buf := []byte{}                       // create a buffer for very long lines
	scanner.Buffer(buf, SpdxMaxBytesLine) // set the max size of that buffer
	for scanner.Scan() {
		// In an effort to short-circuit things if needed, we run a
		// check ourselves and break out early if we see that we have
		// cancelled early.
		select {
		case <-ctx.Done():
			return nil, errwrap.Wrapf(ctx.Err(), "scanner ended early")
		default:
		}

		s := scanner.Text()                           // newlines will be stripped here
		strs := strings.SplitN(s, magicStringSPDX, 2) // max split of 2
		if len(strs) == 1 {                           // no split happened, string not found
			continue
		}

		// weird way to parse, but whatever:
		// "if prefixed by more than n characters, it's probably not a
		// short-form ID; it's probably code to detect short-form IDs."
		if len(stripTrash(strs[0])) > magicNumberSPDX { // arbitrary wat
			continue
		}

		// spdx says: "stop before trailing */ if it is present"
		lid := strings.Split(strs[1], "*/")[0] // lid is licenseID
		lid = strings.TrimSpace(lid)
		lid = stripTrash(lid)

		licenseMap[lid] = struct{}{}
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
				Origin: "", // unknown!
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
		return nil, errwrap.Wrapf(scannerErr, "spdx scanner error")
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
	return result, errwrap.Wrapf(scannerErr, "spdx scanner error")
}

// stripTrash is an improved version of the identically named function in the
// SPDX tools repository.
func stripTrash(lid string) string {
	return stripTrashSPDX.ReplaceAllString(lid, "")
}
