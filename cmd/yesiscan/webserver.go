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

package main

import (
	"context"
	"fmt"

	"github.com/awslabs/yesiscan/webserver"

	cli "github.com/urfave/cli/v2" // imports as package "cli"
)

// Webserver is the general entry point for running this software as an http
// web server.
// TODO: replace the *cli.Context with a more general context that can be used
// by all the different frontends.
func Webserver(c *cli.Context, program string, debug bool, logf func(format string, v ...interface{})) error {

	server := &webserver.Server{
		Program: program,

		Debug: debug,
		Logf: func(format string, v ...interface{}) {
			//logf(format, v...) // XXX: replaced for now
			fmt.Printf(format, v...) // avoid prefixing for now
		},
	}

	ctx := context.TODO() // XXX !!!

	return server.Run(ctx)
}
