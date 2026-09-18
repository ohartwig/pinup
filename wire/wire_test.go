// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package wire

import (
	"testing"

	"github.com/ohartwig/pinup/model"
)

// The name set is what a configuration may say in matchDatasources; it must
// be answerable without a client, and it must carry the configuration's
// own customDatasources under their custom. prefix.
func TestDatasourceNamesNeedsNoClient(t *testing.T) {
	names := DatasourceNames(map[string]model.CustomDatasource{"koh-apk": {Name: "koh-apk"}})
	for _, want := range []string{"docker", "npm", "gitlab-tags", "custom.koh-apk"} {
		if !names[want] {
			t.Errorf("%q is not a datasource name", want)
		}
	}
	if names["nonesuch"] {
		t.Error("an unknown name is a datasource")
	}
}
