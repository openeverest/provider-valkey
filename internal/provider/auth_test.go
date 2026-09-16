// Copyright (C) 2026 The OpenEverest Contributors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package provider

import (
	"strings"
	"testing"
)

func TestGeneratePassword(t *testing.T) {
	const length = 32

	first, err := generatePassword(length)
	if err != nil {
		t.Fatalf("generatePassword returned error: %v", err)
	}
	if len(first) != length {
		t.Fatalf("expected password length %d, got %d", length, len(first))
	}

	// Every character must come from the URI-safe charset so the password can
	// be embedded in a connection URI without escaping.
	for _, r := range first {
		if !strings.ContainsRune(passwordCharset, r) {
			t.Fatalf("password contains character %q outside the allowed charset", r)
		}
	}

	// Successive calls must not repeat.
	second, err := generatePassword(length)
	if err != nil {
		t.Fatalf("generatePassword returned error: %v", err)
	}
	if first == second {
		t.Fatal("expected distinct passwords from successive calls")
	}
}
