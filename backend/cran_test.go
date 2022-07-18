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

// TODO: should this be a subpackage?

package backend_test

import (
	"reflect"
	"testing"

	"github.com/awslabs/yesiscan/backend"
)

// CranDescriptionFileParser parses the string in the License field to get
// License names from DESCRIPTION files. If no Licenses are found nil is
// returned and any files mentioned are ignored by the parser.
func TestCranDescriptionFileParser(t *testing.T) {
	errVal := backend.ErrInvalidLicenseFormat
	tests := []struct {
		input  string
		output []string
		err    error
	}{
		{"", nil, errVal},
		{"||||||", nil, errVal},
		{"++--###", nil, errVal},
		{"file LICENSE", nil, nil},
		{"file any", nil, nil},
		{"MIT + file LICENSE", []string{"MIT"}, nil},
		{"MIT + file LICENSE | file LICENSE", []string{"MIT"}, nil},
		{"Artistic-2.0 | AGPL-3 + file LICENSE", []string{"Artistic-2.0", "AGPL-3"}, nil},
		{"GPL-2 | \n file LICENSE", []string{"GPL-2"}, nil},
		{"MIT + file LICENSE | file LICENSE | AGPL-3 + file anything", []string{"MIT", "AGPL-3"}, nil},
		{"Artistic-2.0 | AGPL-3 + file any | MIT + file LICENSE", []string{"Artistic-2.0", "AGPL-3", "MIT"}, nil},
		{"Artistic-2.0 | | MIT +file LICENSE", []string{"Artistic-2.0", "MIT"}, errVal},
		{"Artistic-2.0 | \n AGPL-3 + file  any | \n MIT + file LICENSE", []string{"Artistic-2.0", "AGPL-3", "MIT"}, nil},
		{"Artistic-2.0 | \n AGPL-3 \n + file any | \n MI\nT + file LICENSE", []string{"Artistic-2.0", "AGPL-3", "MIT"}, nil},
		{"Artistic-2.0 | \n AGPL-3 \n + file any | -+-+##& | \n MIT + file LICENSE", []string{"Artistic-2.0", "AGPL-3", "MIT"}, errVal},
	}

	for _, test := range tests {
		output, err := backend.CranDescriptionFileParser(test.input)
		if err != test.err {
			t.Errorf("Error %v, Correct Error %v", err, test.err)
			continue
		}
		if !reflect.DeepEqual(output, test.output) {
			t.Logf("Output %v, Correct Output %v", output, test.output)
			continue
		}
		t.Logf("Success!")
	}
}
