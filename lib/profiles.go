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

package lib

import (
	"fmt"
	"sort"
	"strings"

	"github.com/awslabs/yesiscan/interfaces"
	"github.com/awslabs/yesiscan/util"
	"github.com/awslabs/yesiscan/util/licenses"

	colour "github.com/fatih/color"
)

const (
	// UseColour specifies whether we use ANSI/HTML colours or not.
	UseColour = true

	// DefaultProfileName is the name given to the built-in "include all"
	// profile.
	DefaultProfileName = "default"
)

// ProfileConfig is the datastructure representing the profile config that is
// used for the .json files on disk.
type ProfileConfig struct {

	// Licenses is the list of license SPDX ID's to match.
	Licenses []string `json:"licenses"`

	// Exclude these licenses from match instead of including by default.
	Exclude bool `json:"exclude"`

	// Comment adds a user friendly comment for this file.
	Comment string `json:"comment"`
}

// ProfileData is the parsed version of ProfileConfig with real license structs.
type ProfileData struct {

	// Licenses is the list of license SPDX ID's to match.
	Licenses []*licenses.License

	// Exclude these licenses from match instead of including by default.
	Exclude bool
}

// SimpleProfiles is a simple way to filter the results. This is the first
// filter function created and is mostly used for an initial POC. It is the
// more complicated successor to the SimpleResults function. Style can be
// `ansi`, `html`, or `text`.
func SimpleProfiles(results interfaces.ResultSet, passes []string, warnings map[string]error, profile *ProfileData, summary bool, backendWeights map[interfaces.Backend]float64, style string) (string, error) {
	if style != "ansi" && style != "html" && style != "text" {
		return "", fmt.Errorf("invalid style: %s", style)
	}

	redString := func(format string, a ...interface{}) string {
		if style == "ansi" {
			return colour.New(colour.FgRed).Add(colour.Bold).Sprintf(format, a...)
		}
		if style == "html" {
			return `<span style="color: red;">` + fmt.Sprintf(format, a...) + "</span>"
		}
		return fmt.Sprintf(format, a...)
	}
	boldString := func(format string, a ...interface{}) string {
		if style == "ansi" {
			return colour.New(colour.Bold).Sprintf(format, a...)
		}
		if style == "html" {
			return `<span style="font-weight: bold;">` + fmt.Sprintf(format, a...) + "</span>"
		}
		return fmt.Sprintf(format, a...)
	}
	str := ""

	countStr := fmt.Sprintf("%d", len(passes))
	if len(passes) > 0 {
		countStr = redString(countStr)
	}

	hasResults := false                  // do we have anything to show?
	licenseMap := make(map[string]int64) // for computing a summary
	errorMap := make(map[string]struct {
		backend string
		err     error
	}) // for recording found skip errors
	// XXX: handle dir's in here specially and merge in their weights with child paths!
Loop:
	for uri, m := range results { // FIXME: sort and process properly
		bs := []*AnnotatedBackend{}
		ttl := 0.0      // total weight for the set of backends at this uri
		skipUri := true // assume we skip
		innerLicenseMap := make(map[string]int64)
		plus := func(name string) {
			val, _ := innerLicenseMap[name] // defaults to zero!
			innerLicenseMap[name] = val + 1
		}
		for backend, result := range m {
			if result.Skip != nil {
				errorMap[uri] = struct {
					backend string
					err     error
				}{
					backend: backend.String(),
					err:     result.Skip,
				}
			}
			// accounting for licenses summary
			for _, x := range result.Licenses {
				plus(x.String())
			}

			if profile == nil {
				skipUri = false
			} else {
				// TODO: memoize this for performance
				count := len(licenses.Union(profile.Licenses, result.Licenses))
				// are there licenses that match in our profile?
				if count > 0 && !profile.Exclude {
					skipUri = false
				}

				// are there licenses we didn't account for?
				if len(result.Licenses) > count && profile.Exclude {
					skipUri = false
				}
			}

			weight, exists := backendWeights[backend]
			if !exists {
				return "", fmt.Errorf("no weight found for backend: %s", backend.String())
			}
			b := &AnnotatedBackend{
				Backend: backend,
				Weight:  weight,
			}
			bs = append(bs, b)
			ttl += weight
		}
		if skipUri { // we don't want to display this Uri (this file)
			continue Loop
		}
		f := 0.0 // NOTE: confidence *if* the different results agree!
		//for backend, result := range m {
		for _, b := range bs { // for backend, result := range m
			backend := b.Backend
			weight := b.Weight // backendWeights[backend]
			result := m[backend]
			scale := weight / ttl
			b.ScaledConfidence = result.Confidence * scale
			f = f + b.ScaledConfidence
		}

		// merge into to parent accounting
		for k, v := range innerLicenseMap { // map[string]int64
			val, _ := licenseMap[k] // defaults to zero!
			licenseMap[k] = val + v
		}

		// start table row here after the above continue...
		if style == "html" {
			str += "<tr><td>"
		}

		sort.Sort(sort.Reverse(SortedBackends(bs)))
		smartURI := util.SmartURI(uri) // make it useful to click on
		if style == "ansi" {
			hyperlink := util.ShellHyperlinkEncode(uri, smartURI)
			str += fmt.Sprintf("%s (%.2f%%)\n", hyperlink, f*100.0)
		}
		if style == "html" {
			hyperlink := util.HtmlHyperlinkEncode(uri, smartURI)
			str += fmt.Sprintf("%s (%.2f%%)", hyperlink, f*100.0)
		}
		if style == "text" {
			// TODO: can we do better for text output?
			str += fmt.Sprintf("%s (%.2f%%)\n", uri, f*100.0)
		}
		hasResults = true

		if style == "html" {
			str += "<ul>"
		}
		for _, b := range bs { // for backend, result := range m
			backend := b.Backend
			weight := b.Weight // backendWeights[backend]
			result := m[backend]

			l := licenses.Join(result.Licenses)
			if UseColour && profile != nil {
				ll := []string{}
				// only colour the matched ones!
				for _, x := range result.Licenses {
					r := x.String()
					inList := licenses.InList(x, profile.Licenses)
					if inList && !profile.Exclude || !inList && profile.Exclude {
						r = x.String()
						r = redString(r)
					}

					ll = append(ll, r)
				}
				l = strings.Join(ll, ", ")
			}

			s := ""
			if style == "ansi" {
				s = fmt.Sprintf("    %s (%.2f/%.2f)  %s (%.2f%%)\n", backend.String(), weight, ttl, l, result.Confidence*100.0)
			}
			if style == "html" {
				s = fmt.Sprintf("<li>%s (%.2f/%.2f) %s (%.2f%%)</li>", backend.String(), weight, ttl, l, result.Confidence*100.0)
			}
			if style == "text" {
				s = fmt.Sprintf("    %s (%.2f/%.2f)  %s (%.2f%%)\n", backend.String(), weight, ttl, l, result.Confidence*100.0)
			}

			str += s
			hasResults = true
			if !debug {
				continue
			}
			it := result.Meta.Iterator // at least one must be present
			for {
				str += fmt.Sprintf("        %s\n", it)
				hasResults = true
				newIt := it.GetIterator()
				if newIt == nil {
					break
				}
				it = newIt
			}
			if parser := it.GetParser(); parser != nil {
				str += fmt.Sprintf("            %s\n", parser)
				hasResults = true
			}
		}
		if style == "html" {
			str += "</ul>"
			str += "</td></tr>"
		}
	}

	skippedStr := ""
	if style == "ansi" {
		skippedStr = fmt.Sprintf("skipped: %s files/directories\n", countStr)
	}
	if style == "html" {
		s := `<tr><td><table id="summary">`
		s += fmt.Sprintf("<tr><th>skipped: %s files/directories</th></tr>", countStr)
		s += "</table></td></tr>"
		skippedStr = s
	}
	if style == "text" {
		skippedStr = fmt.Sprintf("skipped: %d files/directories\n", countStr)
	}

	erroredStr := ""
	if len(errorMap) > 0 { // keep it in scope
		names := []string{}
		for k := range errorMap { // map[string]error
			names = append(names, k)
		}
		sort.Strings(names)
		if style == "ansi" || style == "text" {
			s := "errors:\n"
			for _, x := range names {
				s += fmt.Sprintf("%s: %s (%s)\n", x, redString(errorMap[x].err.Error()), errorMap[x].backend)
			}
			erroredStr = s
		}
		if style == "html" {
			s := `<tr><td><table id="summary">`
			s += `<tr><th colspan="2">errors:</th></tr>`
			for _, x := range names {
				s += fmt.Sprintf("<tr><td>%s</td><td>%s (%s)</td></tr>", x, redString(errorMap[x].err.Error()), errorMap[x].backend)
			}

			s += "</table></td></tr>"
			erroredStr = s
		}
	}

	warningStr := ""
	if len(warnings) > 0 { // keep it in scope
		names := []string{}
		for k := range warnings { // map[string]error
			names = append(names, k)
		}
		sort.Strings(names)
		if style == "ansi" || style == "text" {
			s := "errors:\n"
			for _, x := range names {
				s += fmt.Sprintf("%s: %s\n", x, redString(warnings[x].Error()))
			}
			warningStr = s
		}
		if style == "html" {
			s := `<tr><td><table id="summary">`
			s += `<tr><th colspan="2">errors:</th></tr>`
			for _, x := range names {
				s += fmt.Sprintf("<tr><td>%s</td><td>%s</td></tr>", x, redString(warnings[x].Error()))
			}

			s += "</table></td></tr>"
			warningStr = s
		}
	}

	noResultsStr := ""
	if !hasResults {
		noResultsStr = "<no results>"
		if style == "html" {
			s := `<tr><td><table id="summary">`
			s += "<tr><th>no results</th></tr>"
			s += "</table></td></tr>"
			noResultsStr = s
		}
	}

	summaryStr := ""
	if summary {
		names := []string{}
		for k := range licenseMap { // map[string]int64
			names = append(names, k)
		}
		sort.Strings(names)
		if style == "ansi" || style == "text" {
			s := boldString("summary:") + "\n"
			for _, x := range names {
				s += fmt.Sprintf("%s: %d\n", x, licenseMap[x])
			}
			summaryStr = s
		}
		if style == "html" {
			s := `<tr><td><table id="summary">`
			s += fmt.Sprintf(`<tr><th colspan="2">%s</th></tr>`, boldString("summary:"))
			for _, x := range names {
				s += fmt.Sprintf("<tr><td>%s</td><td>%d</td></tr>", x, licenseMap[x])
			}

			s += "</table></td></tr>"
			summaryStr = s
		}
	}

	if !hasResults {
		summaryStr = ""
	}
	// glue it all together
	str = skippedStr + warningStr + erroredStr + summaryStr + noResultsStr + str

	return str, nil
}
