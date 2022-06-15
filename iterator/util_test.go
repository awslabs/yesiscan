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

package iterator_test

import (
	"testing"

	"github.com/awslabs/yesiscan/iterator"
)

func TestWhichSuffixInsensitive(t *testing.T) {
	has := ".FoO"
	exp := ".foO"
	suffixList := []string{
		".fooo",
		exp,
		".bar",
		".BAZ",
	}
	if s := iterator.WhichSuffixInsensitive(has, suffixList); s != exp {
		t.Errorf("exp: %s", exp)
		t.Errorf("got: %s", s)
		return
	}
}
