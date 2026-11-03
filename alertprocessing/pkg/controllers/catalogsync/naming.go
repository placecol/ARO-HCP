// Copyright 2026 Microsoft Corporation
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package catalogsync

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	alertprocessingv1alpha1 "github.com/Azure/ARO-HCP/alertprocessing/pkg/apis/alertprocessing/v1alpha1"
)

const (
	// maxNameLength keeps generated names within the RFC-1123 label limit (63 chars), which
	// is also a safe bound for the Azure resource name (the CR name is reused there).
	maxNameLength = 63
	// hashLength is the number of hex characters of the identity hash appended for uniqueness.
	hashLength = 6
)

// ResourceName returns a deterministic, RFC-1123-compliant name for the AlertProcessingRule
// representing (scope, alertRuleName). The full "group/alertname" plus scope is hashed so
// distinct rules that sanitize to the same prefix never collide, and so the same rule always
// maps to the same name (idempotent create/GC).
func ResourceName(scope alertprocessingv1alpha1.Scope, alertRuleName string) string {
	sum := sha256.Sum256([]byte(string(scope) + "\x00" + alertRuleName))
	suffix := hex.EncodeToString(sum[:])[:hashLength]

	// Use the alert name (the part after the last '/') as the human-readable prefix.
	readable := alertRuleName
	if idx := strings.LastIndex(alertRuleName, "/"); idx >= 0 {
		readable = alertRuleName[idx+1:]
	}
	prefix := sanitizeLabel(readable)

	scopePart := sanitizeLabel(string(scope))
	// Reserve room for "-<scope>-<hash>" plus the two separators.
	reserved := len(scopePart) + hashLength + 2
	if max := maxNameLength - reserved; len(prefix) > max {
		prefix = strings.Trim(prefix[:max], "-")
	}
	if prefix == "" {
		prefix = "rule"
	}
	return fmt.Sprintf("%s-%s-%s", prefix, scopePart, suffix)
}

// sanitizeLabel lowercases s and replaces every run of disallowed characters with a single
// '-', then trims leading/trailing '-'.
func sanitizeLabel(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevDash = false
			continue
		}
		if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}
