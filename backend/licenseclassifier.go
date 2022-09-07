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
	"context"
	"fmt"
	"sort"

	"github.com/awslabs/yesiscan/interfaces"
	"github.com/awslabs/yesiscan/util/errwrap"
	"github.com/awslabs/yesiscan/util/licenses"
	"github.com/awslabs/yesiscan/util/safepath"

	"github.com/google/licenseclassifier"
	"github.com/google/licenseclassifier/tools/identify_license/backend"
	"github.com/google/licenseclassifier/tools/identify_license/results"
)

// LicenseClassifier is based on the licenseclassifier project.
type LicenseClassifier struct {
	// This was chosen as it's easier to have the first backend be based on
	// a native golang project, rather than having to play the exec games
	// right away. Some code within is based on their cli code that wraps
	// their lib.

	Debug bool
	Logf  func(format string, v ...interface{})

	// XXX: also match with .header files
	// XXX: what default value do we want here?
	// XXX: what exactly does this do?
	IncludeHeaders bool

	// UseDefaultConfidence specifies whether we should use the default
	// confidence threshold that this library seems to use all the time.
	// I've noticed that without it, it misidentifies a lot of things. But
	// with it, it misses some things entirely, even if it incorrectly
	// identifies them.
	UseDefaultConfidence bool

	// SkipZeroResults tells this backend to avoid erroring when we aren't
	// able to determine if a file matches a known license. Since this
	// particular backend is not good at general file identification, and
	// only good at being presented with actual licenses, this is useful if
	// file filtering is not enabled.
	SkipZeroResults bool
}

func (obj *LicenseClassifier) String() string {
	return "licenseclassifier"
}

func (obj *LicenseClassifier) ScanPath(ctx context.Context, path safepath.Path, info *interfaces.Info) (*interfaces.Result, error) {

	if info.FileInfo.IsDir() { // path.IsDir() should be the same.
		return nil, nil // skip
	}

	filenames := []string{path.Path()}

	threshold := 0.0 // we decide acceptability downstream
	if obj.UseDefaultConfidence {
		threshold = licenseclassifier.DefaultConfidenceThreshold
	}
	forbiddenOnly := true // identify using forbidden licenses archive
	be, err := backend.New(threshold, forbiddenOnly)
	if err != nil {
		be.Close()
		return nil, errwrap.Wrapf(err, "cannot create license classifier")
	}

	// XXX: bug: https://github.com/google/licenseclassifier/issues/28
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if errs := be.ClassifyLicensesWithContext(ctx, filenames, obj.IncludeHeaders); errs != nil {
		be.Close()
		for _, err := range errs {
			if obj.Debug {
				obj.Logf("classify license failed: %v", err)
			}
		}
		return nil, fmt.Errorf("cannot classify licenses")
	}

	results := be.GetResults()
	if len(results) == 0 {
		be.Close()
		if obj.SkipZeroResults {
			return nil, nil
		}
		//return nil, fmt.Errorf("couldn't classify license(s)")
		return nil, interfaces.ErrUnknownLicense
	}

	sort.Sort(results)
	// A match identifies the result of matching a string against a known value.
	// Name		string	// Name of known value that was matched.
	// Confidence	float64	// Confidence percentage.
	// Offset	int	// The offset into the unknown string the match was made.
	// Extent	int	// The length from the offset into the unknown string.
	//for _, r := range results {
	//	log.Printf("%s: %s (confidence: %v, offset: %v, extent: %v)",
	//		r.Filename, r.Name, r.Confidence, r.Offset, r.Extent)
	//	// licenses/AGPL-3.0.txt: AGPL-3.0 (confidence: 0.9999677086024283, offset: 0, extent: 30968)
	//}
	be.Close()
	// This can give us multiple results, sorted by most confident.
	result, err := licenseclassifierResultHelper(results[0])
	if err != nil {
		return nil, err
	}

	// Add more info about the others possibilities to the result.
	more := []*interfaces.Result{}
	for i := 1; i < len(results); i++ {
		r, err := licenseclassifierResultHelper(results[i])
		if err != nil {
			return nil, err
		}
		more = append(more, r)
	}
	if len(more) > 0 {
		result.More = more
	}

	return result, nil
}

func licenseclassifierResultHelper(result *results.LicenseType) (*interfaces.Result, error) {
	if result == nil {
		return nil, fmt.Errorf("got nil result")
	}

	// XXX: This backend seems to return names that aren't valid SPDX ID's.
	// It's also not necessarily guaranteed that the SPDX ID's they do
	// return correspond to the exact same license texts that we expect. We
	// need to (1) ensure the mapping is the same, and (2) check when one of
	// these licenses is not in our SPDX list, and tag it separately.
	license := &licenses.License{
		SPDX: result.Name,
		// TODO: populate other fields here (eg: found license text)
	}
	// FIXME: If license is not in SPDX, add a custom entry.
	// FIXME: https://github.com/google/licenseclassifier/issues/31
	if err := license.Validate(); err != nil {
		//return nil, err
		license = &licenses.License{
			//SPDX: "",
			Origin: "licenseclassifier.google.github.com",
			Custom: result.Name,
			// TODO: populate other fields here (eg: found license text)
		}
	}
	return &interfaces.Result{
		Licenses: []*licenses.License{
			license,
		},
		Confidence: result.Confidence,
	}, nil
}
