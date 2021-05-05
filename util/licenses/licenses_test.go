package licenses_test

import (
	"testing"

	"github.com/awslabs/yesiscan/util/licenses"
)

func TestValidate(t *testing.T) {
	license := licenses.License{
		SPDX: "AGPL-3.0-or-later",
	}
	if err := license.Validate(); err != nil {
		t.Errorf("err: %+v", err)
		return
	}
}

func TestID(t *testing.T) {
	if _, err := licenses.ID("AGPL-3.0-or-later"); err != nil {
		t.Errorf("err: %+v", err)
		return
	}
}
