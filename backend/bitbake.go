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
	"sort"
	"strings"

	"github.com/awslabs/yesiscan/interfaces"
	"github.com/awslabs/yesiscan/util/errwrap"
	"github.com/awslabs/yesiscan/util/licenses"
)

const (
	// BitbakeMaxBytesLine sets a larger maximum for file line scanning than
	// the default of bufio.MaxScanTokenSize which is sort of small.
	BitbakeMaxBytesLine = 1024 * 1024 * 8 // 8 MiB

	// BitbakeLicensePrefix is the string we look for when trying to find a
	// license.
	BitbakeLicensePrefix = `LICENSE = "`

	// BitbakeLicenseSuffix is the terminating string at the end of the
	// line. We must not include the newline here.
	BitbakeLicenseSuffix = `"`

	// BitbakeFilenameSuffix is the file extension used by the bitbake
	// files.
	BitbakeFilenameSuffix = ".bb"
)

// Bitbake is a license backend for the bitbake .bb files which are very
// commonly seen in the yocto project. We use a trivial string parser for
// finding these-- this could be improved significantly if people write fancier
// .bb files, but this should get us 99% of the way there.
type Bitbake struct {
	Debug bool
	Logf  func(format string, v ...interface{})
}

func (obj *Bitbake) String() string {
	return "bitbake"
}

func (obj *Bitbake) ScanData(ctx context.Context, data []byte, info *interfaces.Info) (*interfaces.Result, error) {
	if !strings.HasSuffix(info.FileInfo.Name(), BitbakeFilenameSuffix) {
		return nil, nil // skip
	}

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
	buf := []byte{}                          // create a buffer for very long lines
	scanner.Buffer(buf, BitbakeMaxBytesLine) // set the max size of that buffer
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
		if !strings.HasPrefix(s, BitbakeLicensePrefix) {
			continue
		}
		if !strings.HasSuffix(s, BitbakeLicenseSuffix) {
			continue
		}

		license := s[len(BitbakeLicensePrefix) : len(s)-len(BitbakeLicenseSuffix)]
		if license == "" {
			// TODO: should we warn here?
			continue
		}

		// XXX: i've only seen & in between license strings for now...
		// example: https://git.yoctoproject.org/poky/tree/meta/recipes-devtools/btrfs-tools/btrfs-tools_5.16.2.bb#n10
		lids := strings.Split(license, "&") // lid is licenseID
		for _, x := range lids {
			lid := strings.TrimSpace(x)
			// TODO: should we normalize case here?
			licenseMap[lid] = struct{}{}
		}
	}

	if len(licenseMap) == 0 {
		// NOTE: this is NOT the same as interfaces.ErrUnknownLicense
		// because in this scenario, we're comfortable (ish) the parser
		// is exhaustive at finding a license with this methodology.
		// We want to return nil, but we error only if Scanner.Err() did
		// and so normally this returns nil, nil.
		return nil, errwrap.Wrapf(scanner.Err(), "bitbake scanner error")
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
		// know to expect failures.
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

	result := &interfaces.Result{
		Licenses:   licenseList,
		Confidence: 1.0, // TODO: what should we put here?
	}

	// We perform the strange task of processing any partial results, and
	// returning some even if we errored, because the spdx code seems to
	// think this is better than no results. I'll do the same, but there is
	// no guarantee the calling iterator will use these. (Currently it does
	// not!)
	return result, errwrap.Wrapf(scanner.Err(), "bitbake scanner error")
}
