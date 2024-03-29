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

package iterator

import (
	"strings"
)

// WhichSuffix returns the first suffix with the longest match that is found in
// the input string from the list provided. If none are found, then the empty
// string is returned. The comparisons are done in lower case, but the returned
// suffix is in the original case from the input list.
func WhichSuffixInsensitive(s string, suffixList []string) string {
	suffix := ""
	length := 0
	for _, x := range suffixList {
		if strings.HasSuffix(strings.ToLower(s), strings.ToLower(x)) {
			if l := len(x); l > length {
				suffix = x
				length = l
			}
		}
	}
	return suffix
}
